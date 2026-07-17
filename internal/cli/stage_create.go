package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ardasevinc/mattermost-cli/v2/internal/schema"
	"github.com/ardasevinc/mattermost-cli/v2/internal/stagecontent"
	"github.com/ardasevinc/mattermost-cli/v2/internal/stageoutput"
	"github.com/ardasevinc/mattermost-cli/v2/internal/stagerequest"
	"github.com/ardasevinc/mattermost-cli/v2/internal/stagestore"
	"github.com/ardasevinc/mattermost-cli/v2/internal/staging"
)

type stageCompositionFlags struct {
	dryRun      bool
	requestID   string
	message     string
	attachments []string
}

func newStageCreationCommands(state *rootState) []*cobra.Command {
	return []*cobra.Command{
		newStageSendCommand(state),
		newStageContentPostCommand(state, stagestore.Reply),
		newStageContentPostCommand(state, stagestore.EditPost),
		newStageContentlessPostCommand(state, stagestore.DeletePost),
		newStageReactionCommand(state, stagestore.React),
		newStageReactionCommand(state, stagestore.Unreact),
		newStageConversationCreateCommand(state, stagestore.ResolveDM),
		newStageConversationCreateCommand(state, stagestore.ResolveGroupDM),
	}
}

func newStageConversationCreateCommand(state *rootState, operation stagestore.Operation) *cobra.Command {
	var dryRun bool
	var requestID string
	use, short := "dm-create <username>", "Stage creation or resolution of an exact direct conversation"
	args := cobra.ExactArgs(1)
	if operation == stagestore.ResolveGroupDM {
		use, short = "group-create <username>...", "Stage creation or resolution of an exact group conversation"
		args = func(_ *cobra.Command, values []string) error {
			if len(values) < 2 || len(values) > 100 {
				return invalidFailure("group-create requires between 2 and 100 usernames")
			}
			return nil
		}
	}
	command := &cobra.Command{Use: use, Short: short, Args: args}
	command.Flags().BoolVar(&dryRun, "dry-run", false, "preview without persisting a stage")
	command.Flags().StringVar(&requestID, "request-id", "", "caller-generated replay key")
	command.RunE = func(cmd *cobra.Command, values []string) error {
		if dryRun && requestID != "" {
			return invalidFailure("--request-id cannot be used with --dry-run")
		}
		service, closeStore, err := openStagingService(cmd, state, !dryRun)
		if err != nil {
			return err
		}
		if dryRun {
			defer func() { _ = closeStore() }()
			var preview staging.Preview
			if operation == stagestore.ResolveDM {
				preview, err = service.DryRunResolveDM(cmd.Context(), staging.Target{Conversation: staging.Direct, Selector: staging.ByUsername, Value: values[0]})
			} else {
				preview, err = service.DryRunResolveGroup(cmd.Context(), values)
			}
			return writeStagePreview(state, operation, preview, err)
		}
		var result staging.CreatePostResult
		if operation == stagestore.ResolveDM {
			result, err = service.ResolveDM(cmd.Context(), staging.ResolveDMInput{RequestID: requestID, Target: staging.Target{Conversation: staging.Direct, Selector: staging.ByUsername, Value: values[0]}})
		} else {
			result, err = service.ResolveGroup(cmd.Context(), staging.ResolveGroupInput{RequestID: requestID, Usernames: append([]string(nil), values...)})
		}
		return finishStageCreate(state, result, err, closeStore)
	}
	return command
}

func newStageSendCommand(state *rootState) *cobra.Command {
	command := &cobra.Command{Use: "send", Short: "Stage a new message", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() }}
	command.AddCommand(newStageSendTargetCommand(state, "dm"), newStageSendTargetCommand(state, "group"), newStageSendTargetCommand(state, "channel"))
	return command
}

func newStageSendTargetCommand(state *rootState, kind string) *cobra.Command {
	var flags stageCompositionFlags
	var teamName, teamID string
	use, short := kind+" <target>", "Stage a message to an existing "+kind+" conversation"
	command := &cobra.Command{Use: use, Short: short, Args: cobra.ExactArgs(1)}
	addStageCompositionFlags(command, &flags, true)
	if kind == "channel" {
		command.Flags().StringVar(&teamName, "team", "", "resolve a channel name in this team name")
		command.Flags().StringVar(&teamID, "team-id", "", "resolve a channel name in this exact team ID")
	}
	command.RunE = func(cmd *cobra.Command, args []string) error {
		if teamName != "" && teamID != "" {
			return invalidFailure("--team and --team-id cannot be combined")
		}
		target := staging.Target{Value: args[0]}
		switch kind {
		case "dm":
			target.Conversation, target.Selector = staging.Direct, staging.ByUsername
		case "group":
			target.Conversation, target.Selector = staging.Group, staging.ByID
		case "channel":
			target.Conversation, target.Selector = staging.Channel, staging.ByID
			if teamName != "" || teamID != "" {
				target.Selector = staging.ByName
				target.Team = &staging.TeamSelector{By: staging.ByName, Value: teamName}
				if teamID != "" {
					target.Team.By, target.Team.Value = staging.ByID, teamID
				}
			}
		}
		return runHumanCreatePost(cmd, state, flags, target)
	}
	return command
}

func newStageContentPostCommand(state *rootState, operation stagestore.Operation) *cobra.Command {
	var flags stageCompositionFlags
	use, short := "reply <post-id>", "Stage a reply"
	attachments := true
	if operation == stagestore.EditPost {
		use, short, attachments = "post-edit <post-id>", "Stage an edit to your post", false
	}
	command := &cobra.Command{Use: use, Short: short, Args: cobra.ExactArgs(1)}
	addStageCompositionFlags(command, &flags, attachments)
	command.RunE = func(cmd *cobra.Command, args []string) error {
		if err := validateHumanStageFlags(cmd, flags, attachments); err != nil {
			return err
		}
		if flags.dryRun {
			return runPostDryRun(cmd, state, operation, staging.PostDryRunInput{PostID: args[0]})
		}
		body, err := acquireStageBody(cmd, state, flags.message)
		if err != nil {
			return err
		}
		service, closeStore, err := openStagingService(cmd, state, true)
		if err != nil {
			return err
		}
		var result staging.CreatePostResult
		if operation == stagestore.Reply {
			result, err = service.Reply(cmd.Context(), staging.ReplyInput{RequestID: flags.requestID, PostID: args[0], Body: bytes.NewReader(body), Attachments: stageAttachments(flags.attachments)})
		} else {
			result, err = service.EditPost(cmd.Context(), staging.EditPostInput{RequestID: flags.requestID, PostID: args[0], Body: bytes.NewReader(body)})
		}
		return finishStageCreate(state, result, err, closeStore)
	}
	return command
}

func newStageContentlessPostCommand(state *rootState, operation stagestore.Operation) *cobra.Command {
	var dryRun bool
	var requestID string
	command := &cobra.Command{Use: "post-delete <post-id>", Short: "Stage deletion of your post", Args: cobra.ExactArgs(1)}
	command.Flags().BoolVar(&dryRun, "dry-run", false, "preview without persisting a stage")
	command.Flags().StringVar(&requestID, "request-id", "", "caller-generated replay key")
	command.RunE = func(cmd *cobra.Command, args []string) error {
		if dryRun {
			if requestID != "" {
				return invalidFailure("--request-id cannot be used with --dry-run")
			}
			return runPostDryRun(cmd, state, operation, staging.PostDryRunInput{PostID: args[0]})
		}
		service, closeStore, err := openStagingService(cmd, state, true)
		if err != nil {
			return err
		}
		result, err := service.DeletePost(cmd.Context(), staging.DeletePostInput{RequestID: requestID, PostID: args[0]})
		return finishStageCreate(state, result, err, closeStore)
	}
	return command
}

func newStageReactionCommand(state *rootState, operation stagestore.Operation) *cobra.Command {
	var dryRun bool
	var requestID string
	name := string(operation)
	command := &cobra.Command{Use: name + " <post-id> <emoji>", Short: "Stage a post reaction change", Args: cobra.ExactArgs(2)}
	command.Flags().BoolVar(&dryRun, "dry-run", false, "preview without persisting a stage")
	command.Flags().StringVar(&requestID, "request-id", "", "caller-generated replay key")
	command.RunE = func(cmd *cobra.Command, args []string) error {
		input := staging.ReactionDryRunInput{PostID: args[0], Emoji: args[1]}
		if dryRun {
			if requestID != "" {
				return invalidFailure("--request-id cannot be used with --dry-run")
			}
			return runReactionDryRun(cmd, state, operation, input)
		}
		service, closeStore, err := openStagingService(cmd, state, true)
		if err != nil {
			return err
		}
		mutation := staging.ReactionInput{RequestID: requestID, PostID: args[0], Emoji: args[1]}
		var result staging.CreatePostResult
		if operation == stagestore.React {
			result, err = service.React(cmd.Context(), mutation)
		} else {
			result, err = service.Unreact(cmd.Context(), mutation)
		}
		return finishStageCreate(state, result, err, closeStore)
	}
	return command
}

func addStageCompositionFlags(command *cobra.Command, flags *stageCompositionFlags, attachments bool) {
	command.Flags().BoolVar(&flags.dryRun, "dry-run", false, "preview without reading content or persisting a stage")
	command.Flags().StringVar(&flags.requestID, "request-id", "", "caller-generated replay key")
	command.Flags().StringVar(&flags.message, "message", "", "message text (visible in shell history and process inspection)")
	if attachments {
		command.Flags().StringArrayVar(&flags.attachments, "attachment", nil, "attachment path (repeatable)")
	}
}

func validateHumanStageFlags(cmd *cobra.Command, flags stageCompositionFlags, attachments bool) error {
	if flags.dryRun && (cmd.Flags().Changed("message") || len(flags.attachments) != 0 || flags.requestID != "") {
		return invalidFailure("--dry-run cannot be combined with content, attachments, or --request-id")
	}
	if !attachments && len(flags.attachments) != 0 {
		return invalidFailure("attachments are not supported for this operation")
	}
	return nil
}

func runHumanCreatePost(cmd *cobra.Command, state *rootState, flags stageCompositionFlags, target staging.Target) error {
	if err := validateHumanStageFlags(cmd, flags, true); err != nil {
		return err
	}
	if flags.dryRun {
		service, closeStore, err := openStagingService(cmd, state, false)
		if err != nil {
			return err
		}
		defer closeStore()
		preview, err := service.DryRunCreatePost(cmd.Context(), staging.DryRunInput{Target: target})
		return writeStagePreview(state, stagestore.CreatePost, preview, err)
	}
	body, err := acquireStageBody(cmd, state, flags.message)
	if err != nil {
		return err
	}
	service, closeStore, err := openStagingService(cmd, state, true)
	if err != nil {
		return err
	}
	result, err := service.CreatePost(cmd.Context(), staging.CreatePostInput{RequestID: flags.requestID, Target: target, Body: bytes.NewReader(body), Attachments: stageAttachments(flags.attachments)})
	return finishStageCreate(state, result, err, closeStore)
}

func acquireStageBody(cmd *cobra.Command, state *rootState, message string) ([]byte, error) {
	body, err := stagecontent.Acquire(cmd.Context(), stagecontent.Request{Stdin: state.streams.in, Message: message, MessageSet: cmd.Flags().Changed("message"), Machine: state.flags.json}, stagecontent.Runtime{})
	if err == nil {
		return body, nil
	}
	switch {
	case errors.Is(err, stagecontent.ErrConflictingSources):
		return nil, invalidFailure("choose exactly one message source")
	case errors.Is(err, stagecontent.ErrContentRequired):
		return nil, invalidFailure("message content is required")
	case errors.Is(err, stagecontent.ErrEditorNotConfigured):
		return nil, invalidFailure("set VISUAL or EDITOR, pipe content, or use --message")
	case errors.Is(err, stagecontent.ErrEditorFailed):
		return nil, invalidFailure("editor exited without producing an accepted message")
	default:
		return nil, invalidFailure("message content could not be accepted")
	}
}

func stageAttachments(paths []string) []staging.Attachment {
	result := make([]staging.Attachment, len(paths))
	for index, path := range paths {
		result[index] = staging.Attachment{Path: path}
	}
	return result
}

func openStagingService(cmd *cobra.Command, state *rootState, persist bool) (*staging.Service, func() error, error) {
	runtime, err := state.runtimeFor(cmd)
	if err != nil {
		return nil, func() error { return nil }, err
	}
	var store *stagestore.Store
	var storeDependency staging.Store
	if persist {
		paths, pathErr := storePaths(state)
		if pathErr != nil {
			return nil, func() error { return nil }, pathErr
		}
		store, err = stagestore.Open(cmd.Context(), paths.DBPath)
		if err != nil {
			return nil, func() error { return nil }, localStateFailure{fmt.Errorf("could not safely open stage store")}
		}
		if err = expireConfiguredStages(cmd, state, store); err != nil {
			_ = store.Close()
			return nil, func() error { return nil }, err
		}
		storeDependency = store
	}
	closeStore := func() error {
		if store != nil {
			return store.Close()
		}
		return nil
	}
	service, err := staging.New(runtime.Config.URL, "", state.credentials, runtime.Users, runtime.Channels, runtime.Teams, runtime.Posts, storeDependency)
	if err != nil {
		_ = closeStore()
		return nil, func() error { return nil }, classifyStageError(err)
	}
	return service, closeStore, nil
}

func runPostDryRun(cmd *cobra.Command, state *rootState, operation stagestore.Operation, input staging.PostDryRunInput) error {
	service, closeStore, err := openStagingService(cmd, state, false)
	if err != nil {
		return err
	}
	defer func() { _ = closeStore() }()
	var preview staging.Preview
	switch operation {
	case stagestore.Reply:
		preview, err = service.DryRunReply(cmd.Context(), input)
	case stagestore.EditPost:
		preview, err = service.DryRunEditPost(cmd.Context(), input)
	case stagestore.DeletePost:
		preview, err = service.DryRunDeletePost(cmd.Context(), input)
	default:
		return internalFailure(errors.New("unsupported stage operation"))
	}
	return writeStagePreview(state, operation, preview, err)
}

func runReactionDryRun(cmd *cobra.Command, state *rootState, operation stagestore.Operation, input staging.ReactionDryRunInput) error {
	service, closeStore, err := openStagingService(cmd, state, false)
	if err != nil {
		return err
	}
	defer func() { _ = closeStore() }()
	var preview staging.Preview
	if operation == stagestore.React {
		preview, err = service.DryRunReact(cmd.Context(), input)
	} else {
		preview, err = service.DryRunUnreact(cmd.Context(), input)
	}
	return writeStagePreview(state, operation, preview, err)
}

func runStructuredStage(cmd *cobra.Command, state *rootState) error {
	decoder, err := stagerequest.NewDecoder()
	if err != nil {
		return internalFailure(err)
	}
	request, err := decoder.DecodeStage(state.streams.in)
	if err != nil {
		if schema.IsInputReadError(err) {
			return readFailure(errors.New("could not read stage request"))
		}
		return invalidFailure("invalid stage request")
	}
	return dispatchStructuredStage(cmd, state, request)
}

func dispatchStructuredStage(cmd *cobra.Command, state *rootState, request stagerequest.StageRequest) error {
	service, closeStore, err := openStagingService(cmd, state, request.Persist)
	if err != nil {
		return err
	}
	if !request.Persist {
		defer func() { _ = closeStore() }()
		return dispatchStructuredPreview(cmd.Context(), state, service, request)
	}
	var result staging.CreatePostResult
	switch request.Operation {
	case stagerequest.CreatePost:
		input, conversionErr := request.CreatePostInput()
		if conversionErr != nil {
			return invalidFailure("invalid stage request")
		}
		result, err = service.CreatePost(cmd.Context(), input)
	case stagerequest.Reply:
		input, conversionErr := request.ReplyInput()
		if conversionErr != nil {
			return invalidFailure("invalid stage request")
		}
		result, err = service.Reply(cmd.Context(), input)
	case stagerequest.EditPost:
		input, conversionErr := request.EditPostInput()
		if conversionErr != nil {
			return invalidFailure("invalid stage request")
		}
		result, err = service.EditPost(cmd.Context(), input)
	case stagerequest.DeletePost:
		input, conversionErr := request.DeletePostInput()
		if conversionErr != nil {
			return invalidFailure("invalid stage request")
		}
		result, err = service.DeletePost(cmd.Context(), input)
	case stagerequest.React, stagerequest.Unreact:
		input, conversionErr := request.ReactionInput()
		if conversionErr != nil {
			return invalidFailure("invalid stage request")
		}
		if request.Operation == stagerequest.React {
			result, err = service.React(cmd.Context(), input)
		} else {
			result, err = service.Unreact(cmd.Context(), input)
		}
	case stagerequest.ResolveDM:
		target, conversionErr := request.ResolveDMTarget()
		if conversionErr != nil {
			return invalidFailure("invalid stage request")
		}
		result, err = service.ResolveDM(cmd.Context(), staging.ResolveDMInput{RequestID: *request.RequestID, Target: target})
	case stagerequest.ResolveGroupDM:
		usernames, conversionErr := request.ResolveGroupUsernames()
		if conversionErr != nil {
			return invalidFailure("invalid stage request")
		}
		result, err = service.ResolveGroup(cmd.Context(), staging.ResolveGroupInput{RequestID: *request.RequestID, Usernames: usernames})
	default:
		return invalidFailure("unsupported stage operation")
	}
	return finishStageCreate(state, result, err, closeStore)
}

func dispatchStructuredPreview(ctx context.Context, state *rootState, service *staging.Service, request stagerequest.StageRequest) error {
	var preview staging.Preview
	var operation stagestore.Operation
	var err error
	switch request.Operation {
	case stagerequest.CreatePost:
		operation = stagestore.CreatePost
		input, conversionErr := request.DryRunCreatePostInput()
		if conversionErr != nil {
			return invalidFailure("invalid stage request")
		}
		preview, err = service.DryRunCreatePost(ctx, input)
	case stagerequest.Reply, stagerequest.EditPost, stagerequest.DeletePost:
		operation = mapStageOperation(request.Operation)
		input, conversionErr := request.PostDryRunInput()
		if conversionErr != nil {
			return invalidFailure("invalid stage request")
		}
		switch request.Operation {
		case stagerequest.Reply:
			preview, err = service.DryRunReply(ctx, input)
		case stagerequest.EditPost:
			preview, err = service.DryRunEditPost(ctx, input)
		case stagerequest.DeletePost:
			preview, err = service.DryRunDeletePost(ctx, input)
		}
	case stagerequest.React, stagerequest.Unreact:
		operation = mapStageOperation(request.Operation)
		input, conversionErr := request.ReactionDryRunInput()
		if conversionErr != nil {
			return invalidFailure("invalid stage request")
		}
		if request.Operation == stagerequest.React {
			preview, err = service.DryRunReact(ctx, input)
		} else {
			preview, err = service.DryRunUnreact(ctx, input)
		}
	case stagerequest.ResolveDM:
		operation = stagestore.ResolveDM
		target, conversionErr := request.ResolveDMTarget()
		if conversionErr != nil {
			return invalidFailure("invalid stage request")
		}
		preview, err = service.DryRunResolveDM(ctx, target)
	case stagerequest.ResolveGroupDM:
		operation = stagestore.ResolveGroupDM
		usernames, conversionErr := request.ResolveGroupUsernames()
		if conversionErr != nil {
			return invalidFailure("invalid stage request")
		}
		preview, err = service.DryRunResolveGroup(ctx, usernames)
	default:
		return invalidFailure("unsupported stage operation")
	}
	return writeStagePreview(state, operation, preview, err)
}

func mapStageOperation(operation stagerequest.Operation) stagestore.Operation {
	return map[stagerequest.Operation]stagestore.Operation{
		stagerequest.CreatePost: stagestore.CreatePost, stagerequest.Reply: stagestore.Reply,
		stagerequest.EditPost: stagestore.EditPost, stagerequest.DeletePost: stagestore.DeletePost,
		stagerequest.React: stagestore.React, stagerequest.Unreact: stagestore.Unreact,
		stagerequest.ResolveDM: stagestore.ResolveDM, stagerequest.ResolveGroupDM: stagestore.ResolveGroupDM,
	}[operation]
}

func writeStagePreview(state *rootState, operation stagestore.Operation, preview staging.Preview, err error) error {
	if err != nil {
		return classifyStageError(err)
	}
	document, err := stageoutput.NewPreview(operation, preview, state.credentials)
	if err != nil {
		return localStateFailure{errors.New("stage preview is invalid")}
	}
	if state.flags.json {
		return writeStageJSON(state, document)
	}
	destination, _ := json.Marshal(document.Destination)
	lines := []string{"dry run: no stage persisted", "operation: " + safeStoreValue(state, document.Operation), "destination: " + safeStoreValue(state, string(destination)), "content validated: no", "plan:"}
	for _, step := range document.Plan.Steps {
		lines = append(lines, fmt.Sprintf("  %d. %s (%s)", step.Ordinal, safeStoreValue(state, step.Type), safeStoreValue(state, step.Condition)))
	}
	return writeAll(state.streams.out, []byte(strings.Join(lines, "\n")+"\n"))
}

func writeStageCreateResult(state *rootState, result staging.CreatePostResult, err error) error {
	if err != nil {
		return classifyStageError(err)
	}
	document, err := stageoutput.NewCreateReceipt(result, state.credentials)
	if err != nil {
		return localStateFailure{errors.New("stored stage receipt is invalid")}
	}
	if state.flags.json {
		return writeStageJSON(state, document)
	}
	destination, _ := json.Marshal(document.Stage.Destination)
	lines := []string{
		"staged: " + safeStoreValue(state, document.Stage.StageRef),
		"operation: " + safeStoreValue(state, document.Stage.Operation),
		"destination: " + safeStoreValue(state, string(destination)),
		"replayed: " + fmt.Sprint(document.Replayed),
		"apply: mm apply " + safeStoreValue(state, document.Stage.StageRef),
	}
	return writeAll(state.streams.out, []byte(strings.Join(lines, "\n")+"\n"))
}

func finishStageCreate(state *rootState, result staging.CreatePostResult, operationErr error, closeStore func() error) error {
	if err := closeStore(); err != nil {
		return localStateFailure{errors.New("could not close stage store safely")}
	}
	return writeStageCreateResult(state, result, operationErr)
}

func classifyStageError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return readFailure(errors.New("stage operation canceled before persistence"))
	}
	switch {
	case errors.Is(err, stagestore.ErrInvalid):
		return invalidFailure("invalid stage request")
	case errors.Is(err, stagestore.ErrConflict):
		return localStateFailure{errors.New("stage request conflicts with durable local state")}
	case errors.Is(err, stagestore.ErrNotFound):
		return localStateFailure{errors.New("stage not found")}
	case errors.Is(err, stagestore.ErrNotEligible):
		return localStateFailure{errors.New("stage lifecycle transition is not allowed")}
	case errors.Is(err, staging.ErrInvalid):
		return invalidFailure("invalid stage request")
	case errors.Is(err, staging.ErrInput):
		return invalidFailure("message or attachment input was rejected")
	case errors.Is(err, staging.ErrCredential):
		return invalidFailure("protected Mattermost credential present in staged input")
	case errors.Is(err, staging.ErrTarget):
		return readFailure(errors.New("could not resolve the exact Mattermost target"))
	case errors.Is(err, staging.ErrConflict):
		return localStateFailure{errors.New("stage request conflicts with durable local state")}
	case errors.Is(err, staging.ErrNotFound):
		return localStateFailure{errors.New("stage not found")}
	case errors.Is(err, staging.ErrNotEligible):
		return localStateFailure{errors.New("stage lifecycle transition is not allowed")}
	case errors.Is(err, staging.ErrStore):
		return localStateFailure{errors.New("could not persist stage state")}
	default:
		return internalFailure(errors.New("stage operation failed"))
	}
}
