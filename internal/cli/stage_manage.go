package cli

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/ardasevinc/mattermost-cli/v2/internal/schema"
	"github.com/ardasevinc/mattermost-cli/v2/internal/stagecontent"
	"github.com/ardasevinc/mattermost-cli/v2/internal/stageinput"
	"github.com/ardasevinc/mattermost-cli/v2/internal/stageoutput"
	"github.com/ardasevinc/mattermost-cli/v2/internal/stagerequest"
	"github.com/ardasevinc/mattermost-cli/v2/internal/stagestore"
	"github.com/ardasevinc/mattermost-cli/v2/internal/staging"
)

func newStageManagementCommands(state *rootState, fromJSON *bool) []*cobra.Command {
	return []*cobra.Command{newStageReviseCommand(state, fromJSON), newStageCancelCommand(state, fromJSON), newStagePruneCommand(state, fromJSON)}
}

func newStageReviseCommand(state *rootState, fromJSON *bool) *cobra.Command {
	var requestID, message string
	var attachments []string
	var clearAttachments, revive bool
	command := &cobra.Command{Use: "revise <stage-id>", Short: "Create a new revision of staged content"}
	command.Args = func(cmd *cobra.Command, args []string) error {
		if *fromJSON {
			return cobra.NoArgs(cmd, args)
		}
		return cobra.ExactArgs(1)(cmd, args)
	}
	command.Flags().StringVar(&requestID, "request-id", "", "caller-generated replay key")
	command.Flags().StringVar(&message, "message", "", "replacement text (visible in shell history and process inspection)")
	command.Flags().StringArrayVar(&attachments, "attachment", nil, "replacement attachment path (repeatable)")
	command.Flags().BoolVar(&clearAttachments, "clear-attachments", false, "replace retained attachments with an empty set")
	command.Flags().BoolVar(&revive, "revive", false, "revive an expired stage while revising it")
	command.RunE = func(cmd *cobra.Command, args []string) error {
		if *fromJSON {
			if anyFlagChanged(cmd, "request-id", "message", "attachment", "clear-attachments", "revive") {
				return invalidFailure("--from-json cannot be combined with human revision flags")
			}
			return runStructuredRevise(cmd, state)
		}
		if clearAttachments && len(attachments) != 0 {
			return invalidFailure("--clear-attachments and --attachment cannot be combined")
		}
		detail, err := readStageDetail(cmd, state, args[0])
		if err != nil {
			return err
		}
		if detail.Operation != stagestore.CreatePost && detail.Operation != stagestore.Reply && detail.Operation != stagestore.EditPost {
			return localStateFailure{errors.New("this stage operation cannot be revised")}
		}
		if detail.Operation == stagestore.EditPost && (clearAttachments || len(attachments) != 0) {
			return invalidFailure("post-edit stages do not support attachments")
		}
		body, err := stagecontent.Acquire(cmd.Context(), stagecontent.Request{
			Stdin: state.streams.in, Message: message, MessageSet: cmd.Flags().Changed("message"), Initial: detail.Body,
		}, stagecontent.Runtime{})
		if err != nil {
			return mapStageContentError(err)
		}
		var replacementAttachments []staging.Attachment
		if clearAttachments {
			replacementAttachments = []staging.Attachment{}
		} else if cmd.Flags().Changed("attachment") {
			replacementAttachments = stageAttachments(attachments)
		}
		input := staging.ReviseInput{StageID: detail.ID, RequestID: requestID, ExpectedRevision: detail.Revision,
			ExpectedDigest: detail.SemanticDigest, Revive: revive, Body: bytes.NewReader(body), Attachments: replacementAttachments}
		return executeStageRevise(cmd, state, input)
	}
	return command
}

func newStageCancelCommand(state *rootState, fromJSON *bool) *cobra.Command {
	var requestID string
	command := &cobra.Command{Use: "cancel <stage-id>", Short: "Cancel an open stage"}
	command.Args = func(cmd *cobra.Command, args []string) error {
		if *fromJSON {
			return cobra.NoArgs(cmd, args)
		}
		return cobra.ExactArgs(1)(cmd, args)
	}
	command.Flags().StringVar(&requestID, "request-id", "", "caller-generated replay key")
	command.RunE = func(cmd *cobra.Command, args []string) error {
		if *fromJSON {
			if cmd.Flags().Changed("request-id") {
				return invalidFailure("--from-json cannot be combined with --request-id")
			}
			return runStructuredCancel(cmd, state)
		}
		detail, err := readStageDetail(cmd, state, args[0])
		if err != nil {
			return err
		}
		return executeStageCancel(cmd, state, staging.CancelInput{StageID: detail.ID, RequestID: requestID, ExpectedRevision: detail.Revision, ExpectedDigest: detail.SemanticDigest})
	}
	return command
}

func newStagePruneCommand(state *rootState, fromJSON *bool) *cobra.Command {
	var olderThan, requestID string
	var abandonRecovery bool
	command := &cobra.Command{Use: "prune [<stage-id>@<revision>]", Short: "Erase eligible retained stage content"}
	command.Args = func(_ *cobra.Command, args []string) error {
		if *fromJSON {
			if len(args) != 0 {
				return invalidFailure("--from-json cannot be combined with a stage reference")
			}
			return nil
		}
		if len(args) > 1 {
			return invalidFailure("stage prune accepts at most one exact stage reference")
		}
		return nil
	}
	command.Flags().StringVar(&olderThan, "older-than", "", "prune terminal stages older than a Go duration such as 720h")
	command.Flags().StringVar(&requestID, "request-id", "", "caller-generated replay key for exact prune")
	command.Flags().BoolVar(&abandonRecovery, "abandon-recovery", false, "deliberately destroy partial or unknown recovery material")
	command.RunE = func(cmd *cobra.Command, args []string) error {
		if *fromJSON {
			if anyFlagChanged(cmd, "older-than", "request-id", "abandon-recovery") {
				return invalidFailure("structured prune cannot be combined with human prune flags")
			}
			decoder, err := stagerequest.NewDecoder()
			if err != nil {
				return internalFailure(err)
			}
			request, err := decoder.DecodePrune(state.streams.in)
			if err != nil {
				return classifyStageRequestDecode(err, "prune")
			}
			input, err := request.PruneInput()
			if err != nil || pruneInputContainsCredential(input, state.credentials) {
				return invalidFailure("invalid stage prune request")
			}
			return executeExactPrune(cmd, state, input)
		}
		if len(args) == 0 {
			if flagChanged(cmd, "request-id") || abandonRecovery {
				return invalidFailure("--request-id and --abandon-recovery require an exact stage reference")
			}
			age := time.Duration(state.stagePruneSeconds) * time.Second
			if flagChanged(cmd, "older-than") {
				var err error
				age, err = time.ParseDuration(olderThan)
				if err != nil || age < time.Second || age%time.Second != 0 {
					return invalidFailure("--older-than must be a positive whole-second Go duration")
				}
			}
			if age <= 0 {
				return invalidFailure("bulk prune requires --older-than or positive stage_prune_after_seconds")
			}
			return executeBulkPrune(cmd, state, age)
		}
		if flagChanged(cmd, "older-than") {
			return invalidFailure("--older-than cannot be combined with an exact stage reference")
		}
		if requestID != "" && !applyRequestIDPattern.MatchString(requestID) {
			return invalidFailure("invalid --request-id")
		}
		stageID, revision, err := parseStageReference(args[0])
		if err != nil {
			return err
		}
		store, err := openExistingStageStore(cmd, state)
		if err != nil {
			return err
		}
		detail, err := store.Show(cmd.Context(), stageID)
		if err != nil {
			_ = store.Close()
			return classifyStageError(err)
		}
		if detail.Revision != revision {
			_ = store.Close()
			return localStateFailure{errors.New("stage revision changed")}
		}
		input := stagestore.PruneInput{StageID: stageID, RequestID: requestID, ExpectedRevision: revision, ExpectedDigest: detail.SemanticDigest, AbandonRecovery: abandonRecovery}
		if pruneInputContainsCredential(input, state.credentials) {
			_ = store.Close()
			return invalidFailure("invalid stage prune request")
		}
		return executeExactPruneWithStore(cmd, state, store, detail.Destination, input)
	}
	return command
}

func executeExactPrune(cmd *cobra.Command, state *rootState, input stagestore.PruneInput) error {
	store, err := openExistingStageStore(cmd, state)
	if err != nil {
		return err
	}
	detail, err := store.Show(cmd.Context(), input.StageID)
	if err != nil {
		_ = store.Close()
		return classifyStageError(err)
	}
	return executeExactPruneWithStore(cmd, state, store, detail.Destination, input)
}

func executeExactPruneWithStore(cmd *cobra.Command, state *rootState, store *stagestore.Store, destination []byte, input stagestore.PruneInput) error {
	result, operationErr := store.Prune(cmd.Context(), input)
	if closeErr := store.Close(); closeErr != nil {
		return localStateFailure{errors.New("could not close stage store safely")}
	}
	if operationErr != nil {
		return classifyRetentionError(operationErr)
	}
	document, err := stageoutput.NewReceipt(result, destination, state.credentials)
	if err != nil {
		return localStateFailure{errors.New("stored stage receipt is invalid")}
	}
	if state.flags.json {
		return writeStageJSON(state, document)
	}
	return writeAll(state.streams.out, []byte(document.Action+": "+safeStoreValue(state, document.Stage.StageRef)+"\n"))
}

func executeBulkPrune(cmd *cobra.Command, state *rootState, age time.Duration) error {
	paths, err := storePaths(state)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	result := stagestore.BulkPruneResult{Schema: "mm/v2/stage-prune-result", Action: "pruned", Cutoff: now.Add(-age), RecordedAt: now}
	if _, err = os.Stat(paths.DBPath); errors.Is(err, fs.ErrNotExist) {
		return writeBulkPruneResult(state, result)
	} else if err != nil {
		return localStateFailure{errors.New("could not inspect stage store")}
	}
	store, err := openExistingStageStore(cmd, state)
	if err != nil {
		return err
	}
	result, err = store.PruneEligible(cmd.Context(), result.Cutoff, result.RecordedAt)
	if closeErr := store.Close(); closeErr != nil {
		return localStateFailure{errors.New("could not close stage store safely")}
	}
	if err != nil {
		return classifyRetentionError(err)
	}
	return writeBulkPruneResult(state, result)
}

func classifyRetentionError(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, stagestore.ErrInvalid) || errors.Is(err, stagestore.ErrConflict) ||
		errors.Is(err, stagestore.ErrNotFound) || errors.Is(err, stagestore.ErrNotEligible) {
		return classifyStageError(err)
	}
	return localStateFailure{errors.New("could not persist stage retention state")}
}

func writeBulkPruneResult(state *rootState, result stagestore.BulkPruneResult) error {
	document, err := stageoutput.NewPruneResult(result, state.credentials)
	if err != nil {
		return localStateFailure{errors.New("stored prune result is invalid")}
	}
	if state.flags.json {
		return writeStageJSON(state, document)
	}
	return writeAll(state.streams.out, []byte(fmt.Sprintf("pruned: %d stages older than %s\n", document.PrunedCount, safeStoreValue(state, document.Cutoff))))
}

func pruneInputContainsCredential(input stagestore.PruneInput, credentials []string) bool {
	values := []string{input.StageID, input.RequestID, hex.EncodeToString(input.ExpectedDigest[:])}
	for _, credential := range credentials {
		if credential == "" {
			continue
		}
		for _, value := range values {
			if strings.Contains(value, credential) {
				return true
			}
		}
	}
	return false
}

func anyFlagChanged(command *cobra.Command, names ...string) bool {
	for _, name := range names {
		if command.Flags().Changed(name) {
			return true
		}
	}
	return false
}

func readStageDetail(cmd *cobra.Command, state *rootState, stageID string) (stagestore.StageDetail, error) {
	store, absent, err := openStageStoreReadOnly(cmd, state)
	if err != nil {
		return stagestore.StageDetail{}, err
	}
	if absent {
		return stagestore.StageDetail{}, localStateFailure{errors.New("stage not found")}
	}
	detail, showErr := store.Show(cmd.Context(), stageID)
	closeErr := store.Close()
	if closeErr != nil {
		return stagestore.StageDetail{}, localStateFailure{errors.New("could not close stage store safely")}
	}
	if errors.Is(showErr, stagestore.ErrInvalid) {
		return stagestore.StageDetail{}, invalidFailure("invalid stage id")
	}
	if errors.Is(showErr, stagestore.ErrNotFound) {
		return stagestore.StageDetail{}, localStateFailure{errors.New("stage not found")}
	}
	if showErr != nil {
		return stagestore.StageDetail{}, localStateFailure{errors.New("could not read stage")}
	}
	return detail, nil
}

func openExistingStageStore(cmd *cobra.Command, state *rootState) (*stagestore.Store, error) {
	paths, err := storePaths(state)
	if err != nil {
		return nil, err
	}
	if _, err = os.Stat(paths.DBPath); errors.Is(err, fs.ErrNotExist) {
		return nil, localStateFailure{errors.New("stage store does not exist")}
	} else if err != nil {
		return nil, localStateFailure{errors.New("could not inspect stage store")}
	}
	store, err := stagestore.Open(cmd.Context(), paths.DBPath)
	if err != nil {
		return nil, localStateFailure{errors.New("could not safely open stage store")}
	}
	if err = expireConfiguredStages(cmd, state, store); err != nil {
		_ = store.Close()
		return nil, err
	}
	return store, nil
}

func expireConfiguredStages(cmd *cobra.Command, state *rootState, store *stagestore.Store) error {
	if state.stageTTLSeconds == 0 {
		return nil
	}
	now := time.Now().UTC()
	if _, err := store.ExpireEligible(cmd.Context(), now.Add(-time.Duration(state.stageTTLSeconds)*time.Second), now); err != nil {
		return localStateFailure{errors.New("could not apply stage retention policy")}
	}
	return nil
}

func executeStageRevise(cmd *cobra.Command, state *rootState, input staging.ReviseInput) error {
	store, err := openExistingStageStore(cmd, state)
	if err != nil {
		return err
	}
	reviser, err := staging.NewReviser(state.credentials, store, stageinput.Bind)
	if err != nil {
		_ = store.Close()
		return classifyStageError(err)
	}
	result, operationErr := reviser.Revise(cmd.Context(), input)
	if closeErr := store.Close(); closeErr != nil {
		return localStateFailure{errors.New("could not close stage store safely")}
	}
	return writeStageRevisionResult(state, result, operationErr)
}

func executeStageCancel(cmd *cobra.Command, state *rootState, input staging.CancelInput) error {
	store, err := openExistingStageStore(cmd, state)
	if err != nil {
		return err
	}
	reviser, err := staging.NewReviser(state.credentials, store, stageinput.Bind)
	if err != nil {
		_ = store.Close()
		return classifyStageError(err)
	}
	result, operationErr := reviser.Cancel(cmd.Context(), input)
	if closeErr := store.Close(); closeErr != nil {
		return localStateFailure{errors.New("could not close stage store safely")}
	}
	return writeStageRevisionResult(state, result, operationErr)
}

func runStructuredRevise(cmd *cobra.Command, state *rootState) error {
	decoder, err := stagerequest.NewDecoder()
	if err != nil {
		return internalFailure(err)
	}
	request, err := decoder.DecodeRevise(state.streams.in)
	if err != nil {
		return classifyStageRequestDecode(err, "revision")
	}
	input, err := request.ReviseInput()
	if err != nil {
		return invalidFailure("invalid stage revision request")
	}
	return executeStageRevise(cmd, state, input)
}

func runStructuredCancel(cmd *cobra.Command, state *rootState) error {
	decoder, err := stagerequest.NewDecoder()
	if err != nil {
		return internalFailure(err)
	}
	request, err := decoder.DecodeCancel(state.streams.in)
	if err != nil {
		return classifyStageRequestDecode(err, "cancel")
	}
	input, err := request.CancelInput()
	if err != nil {
		return invalidFailure("invalid stage cancel request")
	}
	return executeStageCancel(cmd, state, input)
}

func classifyStageRequestDecode(err error, action string) error {
	if schema.IsInputReadError(err) {
		return readFailure(fmt.Errorf("could not read stage %s request", action))
	}
	return invalidFailure("invalid stage " + action + " request")
}

func writeStageRevisionResult(state *rootState, result staging.RevisionResult, err error) error {
	if err != nil {
		return classifyStageError(err)
	}
	document, err := stageoutput.NewReceipt(result.Stored, result.Destination, state.credentials)
	if err != nil {
		return localStateFailure{errors.New("stored stage receipt is invalid")}
	}
	if state.flags.json {
		return writeStageJSON(state, document)
	}
	line := document.Action + ": " + safeStoreValue(state, document.Stage.StageRef) + "\n"
	if document.Action == "revised" {
		line += "apply: mm apply " + safeStoreValue(state, document.Stage.StageRef) + "\n"
	}
	return writeAll(state.streams.out, []byte(line))
}

func mapStageContentError(err error) error {
	switch {
	case errors.Is(err, stagecontent.ErrConflictingSources):
		return invalidFailure("choose exactly one message source")
	case errors.Is(err, stagecontent.ErrContentRequired):
		return invalidFailure("message content is required")
	case errors.Is(err, stagecontent.ErrEditorNotConfigured):
		return invalidFailure("set VISUAL or EDITOR, pipe content, or use --message")
	case errors.Is(err, stagecontent.ErrEditorFailed):
		return invalidFailure("editor exited without producing an accepted message")
	default:
		return invalidFailure("message content could not be accepted")
	}
}
