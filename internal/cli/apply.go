package cli

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ardasevinc/mattermost-cli/v2/internal/api"
	applyservice "github.com/ardasevinc/mattermost-cli/v2/internal/apply"
	"github.com/ardasevinc/mattermost-cli/v2/internal/mattermost"
	"github.com/ardasevinc/mattermost-cli/v2/internal/schema"
	"github.com/ardasevinc/mattermost-cli/v2/internal/stagerequest"
	"github.com/ardasevinc/mattermost-cli/v2/internal/stagestore"
)

var stageReferencePattern = regexp.MustCompile(`^(stg_[A-Za-z0-9_-]{32})@([1-9][0-9]{0,15})$`)
var applyRequestIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._~:-]{0,255}$`)

type applyCommandFailure struct {
	code, recovery, stageRef string
	exit                     int
	err                      error
}

func (e applyCommandFailure) Error() string { return e.err.Error() }
func (e applyCommandFailure) Unwrap() error { return e.err }

func newApplyCommand(state *rootState) *cobra.Command {
	var fromJSON, resumePartial, forceUnknown bool
	var requestID string
	command := &cobra.Command{
		Use:   "apply <stage-id>@<revision>",
		Short: "Apply one exact reviewed stage revision",
		Args: func(_ *cobra.Command, args []string) error {
			if fromJSON {
				state.flags.json = true
			}
			if fromJSON && len(args) != 0 {
				return invalidFailure("--from-json cannot be combined with a stage reference")
			}
			if !fromJSON && len(args) != 1 {
				return invalidFailure("apply requires one exact <stage-id>@<revision> reference")
			}
			return nil
		},
		PreRunE: func(cmd *cobra.Command, _ []string) error {
			if resumePartial && forceUnknown {
				return invalidFailure("--resume-partial and --force-unknown cannot be combined")
			}
			if fromJSON {
				state.flags.json = true
				if flagChanged(cmd, "request-id") || flagChanged(cmd, "resume-partial") || flagChanged(cmd, "force-unknown") {
					return invalidFailure("structured apply cannot be combined with human apply flags")
				}
			} else if flagChanged(cmd, "request-id") && !applyRequestIDPattern.MatchString(requestID) {
				return invalidFailure("invalid --request-id")
			}
			return resolveStageOptions(state, cmd)
		},
	}
	command.Flags().BoolVar(&fromJSON, "from-json", false, "read one versioned apply request from stdin")
	command.Flags().StringVar(&requestID, "request-id", "", "caller-generated replay key")
	command.Flags().BoolVar(&resumePartial, "resume-partial", false, "resume only effects proven not applied")
	command.Flags().BoolVar(&forceUnknown, "force-unknown", false, "accept duplicate risk after an unknown outcome")
	command.RunE = func(cmd *cobra.Command, args []string) error {
		if fromJSON {
			decoder, err := stagerequest.NewDecoder()
			if err != nil {
				return internalFailure(err)
			}
			request, err := decoder.DecodeApply(state.streams.in)
			if err != nil {
				if schema.IsInputReadError(err) {
					return readFailure(errors.New("could not read structured apply request"))
				}
				return invalidFailure("invalid structured apply request")
			}
			claim, err := request.ApplyClaimInput()
			if err != nil {
				return invalidFailure("invalid structured apply request")
			}
			return executeApply(cmd, state, claim, false)
		}

		stageID, revision, err := parseStageReference(args[0])
		if err != nil {
			return err
		}
		mode := stagestore.RecoveryModeOrdinary
		if resumePartial {
			mode = stagestore.RecoveryModePartial
		} else if forceUnknown {
			mode = stagestore.RecoveryModeUnknown
		}
		store, err := openExistingStageStore(cmd, state)
		if err != nil {
			return err
		}
		detail, showErr := store.Show(cmd.Context(), stageID)
		if showErr != nil {
			_ = store.Close()
			return classifyApplyError(args[0], showErr)
		}
		if requestID != "" {
			binding, found, lookupErr := store.LookupApplyRequest(cmd.Context(), detail.ServerURL, detail.UserID, requestID)
			if lookupErr != nil {
				_ = store.Close()
				return classifyApplyErrorWithRecovery(args[0], string(detail.Recovery), lookupErr)
			}
			if found {
				claim := stagerequest.NewApplyClaimInput(stageID, requestID, revision, binding.Attempt.SemanticDigest, mode)
				if binding.Attempt.StageID != stageID || binding.Attempt.Revision != revision || binding.Attempt.RecoveryMode != mode || binding.RequestDigest != claim.RequestDigest {
					_ = store.Close()
					return applyStateConflictWithRecovery(args[0], string(detail.Recovery), "request id conflicts with a different apply intent")
				}
				if closeErr := store.Close(); closeErr != nil {
					return applyStateConflictWithRecovery(args[0], string(detail.Recovery), "could not close stage store safely")
				}
				return executeApply(cmd, state, claim, false)
			}
		}
		if detail.Revision != revision {
			_ = store.Close()
			return applyStateConflict(args[0], "stage revision changed; inspect the stage again")
		}
		if closeErr := store.Close(); closeErr != nil {
			return applyStateConflict(args[0], "could not close stage store safely")
		}
		claim := stagerequest.NewApplyClaimInput(stageID, requestID, revision, detail.SemanticDigest, mode)
		if requestID != "" {
			contract := stagerequest.ApplyRequest{Schema: stagerequest.ApplySchema, RequestID: requestID, StageID: stageID, Revision: stagerequest.ExactInt64(revision), ExpectedDigest: hex.EncodeToString(detail.SemanticDigest[:]), RecoveryMode: string(mode)}
			claim, err = contract.ApplyClaimInput()
			if err != nil {
				return invalidFailure("invalid --request-id")
			}
		}
		return executeApply(cmd, state, claim, true)
	}
	return command
}

func parseStageReference(value string) (string, int64, error) {
	match := stageReferencePattern.FindStringSubmatch(value)
	if match == nil {
		return "", 0, invalidFailure("invalid stage reference; expected <stage-id>@<revision>")
	}
	revision, err := strconv.ParseInt(match[2], 10, 64)
	if err != nil || revision < 1 || revision > 9007199254740991 {
		return "", 0, invalidFailure("invalid stage revision")
	}
	return match[1], revision, nil
}

func executeApply(cmd *cobra.Command, state *rootState, claim stagestore.ApplyClaimInput, human bool) error {
	stageRef := fmt.Sprintf("%s@%d", claim.StageID, claim.Revision)
	store, err := openExistingStageStore(cmd, state)
	if err != nil {
		return err
	}
	runtime, err := state.runtimeFor(cmd)
	if err != nil {
		_ = store.Close()
		return err
	}
	credentials := make([][]byte, 0, len(state.credentials))
	for _, credential := range state.credentials {
		if credential != "" {
			credentials = append(credentials, []byte(credential))
		}
	}
	service, err := applyservice.New(
		strings.TrimRight(runtime.Config.URL, "/")+"/api/v4", "", credentials, store,
		runtime.Users, runtime.Channels, runtime.Posts,
		mattermost.NewConversationMutations(runtime.Client), mattermost.NewPostMutations(runtime.Client),
		applyservice.WithAttachmentExecution(store.StateDir(), mattermost.NewFileMutations(runtime.Client)),
	)
	if err != nil {
		_ = store.Close()
		return internalFailure(errors.New("could not initialize apply service"))
	}
	if human && claim.RecoveryMode == stagestore.RecoveryModeUnknown {
		warning := "warning: forcing an unknown stage may duplicate a real Mattermost side effect; inspect the destination first\n"
		if err := writeAll(state.streams.err, []byte(warning)); err != nil {
			_ = store.Close()
			return err
		}
	}
	receipt, applyErr := service.Apply(cmd.Context(), claim)
	recoveryHint := "none"
	if applyErr != nil {
		if detail, showErr := store.Show(context.WithoutCancel(cmd.Context()), claim.StageID); showErr == nil {
			recoveryHint = string(detail.Recovery)
		}
	}
	closeErr := store.Close()
	if applyErr != nil {
		return classifyApplyErrorWithRecovery(stageRef, recoveryHint, applyErr)
	}
	if closeErr != nil {
		return classifyApplyReceiptCloseFailure(stageRef, receipt)
	}
	if err := writeApplyReceipt(state, receipt); err != nil {
		if receiptConfirmsEffect(receipt) {
			return applyConfirmedFailureWithRecovery(stageRef, string(receipt.Recovery), errors.New("effect confirmed but receipt output failed; do not retry"))
		}
		if receipt.Outcome == stagestore.OutcomeUnknown {
			return applyCommandFailure{"mutation_unknown", "force_unknown", stageRef, 5, errors.New("mutation outcome is unknown and receipt output failed")}
		}
		return err
	}
	switch receipt.Outcome {
	case stagestore.OutcomeSucceeded, stagestore.OutcomeAlreadySatisfied:
		state.setSemanticExit(0)
	case stagestore.OutcomeRejected:
		state.setSemanticExit(4)
	case stagestore.OutcomePartial, stagestore.OutcomeUnknown:
		state.setSemanticExit(5)
	default:
		return internalFailure(errors.New("stored apply receipt has an invalid outcome"))
	}
	return nil
}

func classifyApplyReceiptCloseFailure(stageRef string, receipt stagestore.ApplyReceipt) error {
	switch receipt.Outcome {
	case stagestore.OutcomeUnknown:
		return applyCommandFailure{"mutation_unknown", "force_unknown", stageRef, 5, errors.New("mutation outcome is unknown and the stage store did not close safely; inspect the destination before forcing recovery")}
	case stagestore.OutcomeRejected:
		return applyCommandFailure{"mutation_rejected", string(receipt.Recovery), stageRef, 4, errors.New("mutation was rejected but the stage store did not close safely")}
	}
	if receiptConfirmsEffect(receipt) {
		return applyConfirmedFailureWithRecovery(stageRef, string(receipt.Recovery), errors.New("effect confirmed but the stage store did not close safely; do not retry"))
	}
	return applyStateConflictWithRecovery(stageRef, string(receipt.Recovery), "could not close stage store safely")
}

func classifyApplyError(stageRef string, err error) error {
	return classifyApplyErrorWithRecovery(stageRef, "none", err)
}

func classifyApplyErrorWithRecovery(stageRef, recovery string, err error) error {
	var confirmed *applyservice.ConfirmedEffectError
	var unsafeReceipt *applyservice.UnsafeReceiptError
	var unknown *api.OutcomeUnknownError
	switch {
	case errors.As(err, &unsafeReceipt):
		return classifyUnsafeApplyReceipt(stageRef, unsafeReceipt.UnsafeReceipt())
	case errors.As(err, &confirmed):
		return applyConfirmedFailure(stageRef, err)
	case errors.As(err, &unknown):
		return applyCommandFailure{"mutation_unknown", "force_unknown", stageRef, 5, errors.New("mutation outcome is unknown; inspect the destination before forcing recovery")}
	case errors.Is(err, stagestore.ErrConflict), errors.Is(err, stagestore.ErrNotEligible), errors.Is(err, stagestore.ErrNotFound),
		errors.Is(err, applyservice.ErrTargetDrift), errors.Is(err, applyservice.ErrAttachmentBudget):
		return applyStateConflictWithRecovery(stageRef, recovery, "stage state or remote target changed; inspect the stage again")
	case errors.Is(err, applyservice.ErrCredential):
		return invalidFailure("protected Mattermost credential present in apply input")
	case errors.Is(err, applyservice.ErrInvalid):
		return invalidFailure("invalid apply request")
	case errors.Is(err, applyservice.ErrUnsupportedOperation):
		return applyStateConflict(stageRef, "staged operation is not supported by this build")
	case errors.Is(err, applyservice.ErrJournal):
		return applyStateConflict(stageRef, "could not persist the apply journal")
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return readFailure(errors.New("apply canceled before a confirmed mutation outcome"))
	default:
		var remote *api.APIError
		if errors.As(err, &remote) || errors.Is(err, api.ErrNetwork) || errors.Is(err, api.ErrTimeout) || errors.Is(err, api.ErrInvalidJSON) || errors.Is(err, api.ErrBodyTooLarge) {
			return readFailure(errors.New("could not validate the staged Mattermost target"))
		}
		return applyStateConflict(stageRef, "could not safely apply the staged change")
	}
}

func classifyUnsafeApplyReceipt(stageRef string, receipt stagestore.ApplyReceipt) error {
	switch receipt.Outcome {
	case stagestore.OutcomeSucceeded, stagestore.OutcomeAlreadySatisfied, stagestore.OutcomePartial:
		return applyConfirmedFailureWithRecovery(stageRef, string(receipt.Recovery), errors.New("effect confirmed but its receipt is unsafe to emit; do not retry"))
	case stagestore.OutcomeUnknown:
		return applyCommandFailure{"mutation_unknown", "force_unknown", stageRef, 5, errors.New("mutation outcome is unknown and its receipt is unsafe to emit; inspect the destination before forcing recovery")}
	case stagestore.OutcomeRejected:
		return applyCommandFailure{"mutation_rejected", string(receipt.Recovery), stageRef, 4, errors.New("mutation was rejected but its receipt is unsafe to emit")}
	default:
		return applyStateConflictWithRecovery(stageRef, string(receipt.Recovery), "stored apply receipt has an invalid outcome")
	}
}

func applyStateConflict(stageRef, message string) error {
	return applyStateConflictWithRecovery(stageRef, "none", message)
}

func applyStateConflictWithRecovery(stageRef, recovery, message string) error {
	if recovery != "none" && recovery != "resume_partial" && recovery != "force_unknown" && recovery != "forbidden" {
		recovery = "none"
	}
	return applyCommandFailure{"state_conflict", recovery, stageRef, 6, errors.New(message)}
}

func applyConfirmedFailure(stageRef string, err error) error {
	return applyConfirmedFailureWithRecovery(stageRef, "forbidden", err)

}

func applyConfirmedFailureWithRecovery(stageRef, recovery string, err error) error {
	if recovery != "resume_partial" && recovery != "force_unknown" && recovery != "forbidden" {
		recovery = "forbidden"
	}
	return applyCommandFailure{"confirmed_effect_local_failure", recovery, stageRef, 7, err}
}

func receiptConfirmsEffect(receipt stagestore.ApplyReceipt) bool {
	if receipt.Outcome == stagestore.OutcomeSucceeded || receipt.Outcome == stagestore.OutcomeAlreadySatisfied {
		return true
	}
	for _, step := range receipt.Steps {
		if step.State == stagestore.StepValidated || step.State == stagestore.StepSkipped {
			return true
		}
	}
	return false
}

func writeApplyReceipt(state *rootState, receipt stagestore.ApplyReceipt) error {
	raw, err := json.Marshal(receipt)
	if err != nil {
		return internalFailure(errors.New("could not encode apply receipt"))
	}
	registry, err := schema.Load()
	if err != nil {
		return internalFailure(err)
	}
	if err := registry.Validate("mm/v2/apply-receipt", bytes.NewReader(raw)); err != nil {
		return internalFailure(errors.New("stored apply receipt is invalid"))
	}
	if state.flags.json {
		return writeAll(state.streams.out, append(raw, '\n'))
	}
	lines := []string{
		"attempt: " + safeStoreValue(state, receipt.AttemptID),
		fmt.Sprintf("stage: %s@%d", safeStoreValue(state, receipt.StageID), receipt.Revision),
		"operation: " + safeStoreValue(state, string(receipt.Operation)),
		"outcome: " + safeStoreValue(state, string(receipt.Outcome)),
		"recovery: " + safeStoreValue(state, string(receipt.Recovery)),
		"replayed: " + strconv.FormatBool(receipt.Replay),
		"steps:",
	}
	for _, step := range receipt.Steps {
		line := fmt.Sprintf("  %d. %s %s", step.Ordinal, safeStoreValue(state, step.Kind), safeStoreValue(state, string(step.State)))
		if step.ReusedFrom != nil {
			line += fmt.Sprintf(" reused-from=%s/%d", safeStoreValue(state, step.ReusedFrom.AttemptID), step.ReusedFrom.Ordinal)
		}
		if len(step.Result) != 0 && string(step.Result) != "null" {
			line += " result=" + safeStoreValue(state, string(step.Result))
		}
		lines = append(lines, line)
	}
	stageRef := fmt.Sprintf("%s@%d", safeStoreValue(state, receipt.StageID), receipt.Revision)
	next := "none (do not retry)"
	switch receipt.Recovery {
	case stagestore.RecoveryNone:
		next = "mm apply " + stageRef
	case stagestore.RecoveryPartial:
		next = "mm apply " + stageRef + " --resume-partial"
	case stagestore.RecoveryUnknown:
		next = "mm apply " + stageRef + " --force-unknown"
	}
	lines = append(lines, "next: "+next)
	return writeAll(state.streams.out, []byte(strings.Join(lines, "\n")+"\n"))
}
