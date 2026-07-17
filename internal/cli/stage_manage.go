package cli

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/spf13/cobra"

	"github.com/ardasevinc/mattermost-cli/internal/schema"
	"github.com/ardasevinc/mattermost-cli/internal/stagecontent"
	"github.com/ardasevinc/mattermost-cli/internal/stageinput"
	"github.com/ardasevinc/mattermost-cli/internal/stageoutput"
	"github.com/ardasevinc/mattermost-cli/internal/stagerequest"
	"github.com/ardasevinc/mattermost-cli/internal/stagestore"
	"github.com/ardasevinc/mattermost-cli/internal/staging"
)

func newStageManagementCommands(state *rootState, fromJSON *bool) []*cobra.Command {
	return []*cobra.Command{newStageReviseCommand(state, fromJSON), newStageCancelCommand(state, fromJSON)}
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
	return store, nil
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
