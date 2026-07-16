package cli

import (
	"fmt"
	"regexp"

	"github.com/spf13/cobra"

	"github.com/ardasevinc/mattermost-cli/internal/output"
	"github.com/ardasevinc/mattermost-cli/internal/retrieval"
)

var safePostIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

func newThreadCommand(state *rootState) *cobra.Command {
	return &cobra.Command{
		Use: "thread <post-id>", Short: "Fetch and display a specific thread", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error { return runThread(cmd, state, args[0]) },
	}
}

func runThread(cmd *cobra.Command, state *rootState, postID string) error {
	if !safePostIDPattern.MatchString(postID) {
		return invalidFailure("post ID must be a nonempty Mattermost identifier")
	}
	display, err := state.readDisplay(cmd)
	if err != nil {
		return err
	}
	runtime, err := state.runtimeFor(cmd)
	if err != nil {
		return err
	}
	if err := emitRedactionWarning(state, runtime, display.json); err != nil {
		return err
	}
	me, err := runtime.Users.Current(cmd.Context())
	if err != nil {
		return readFailure(err)
	}
	result, err := retrieval.Thread(cmd.Context(), runtime.Posts, postID)
	if err != nil {
		return readFailure(err)
	}
	if len(result.Posts) == 0 {
		return readError("thread was not found or is empty")
	}
	rootIndex := -1
	channelID := result.Posts[0].ChannelID
	requestedRootID := postID
	for index := range result.Posts {
		if result.Posts[index].ThreadShapeKnown && result.Posts[index].RootID == "" {
			rootIndex = index
			requestedRootID = result.Posts[index].ID
			channelID = result.Posts[index].ChannelID
			break
		}
		if result.Posts[index].ThreadShapeKnown && result.Posts[index].RootID != "" {
			requestedRootID = result.Posts[index].RootID
		}
	}
	complete := result.Completeness == retrieval.CompletenessComplete && rootIndex >= 0
	effectiveCompleteness := result.Completeness
	if rootIndex < 0 && effectiveCompleteness == retrieval.CompletenessComplete {
		effectiveCompleteness = retrieval.CompletenessUnknown
	}
	threadMetadata := retrieval.VisibleThreadsMetadata{Status: retrieval.VisibleThreadsPartial, FailedRootIDs: []string{requestedRootID}}
	if complete {
		threadMetadata = retrieval.VisibleThreadsMetadata{Status: retrieval.VisibleThreadsComplete, HydratedRootCount: 1, FailedRootIDs: []string{}}
	}
	messages, redactions, err := normalizeReadPosts(cmd.Context(), runtime, result.Posts, me.ID)
	if err != nil {
		return readFailure(err)
	}
	messages = output.GroupIntoThreads(messages)
	channel, channelRedactions, unavailable, err := resolvedReadChannel(cmd.Context(), runtime, channelID, me.ID)
	if err != nil {
		return readFailure(err)
	}
	redactions = append(channelRedactions, redactions...)
	presentedThreads, threadRedactions := processedVisibleThreads(threadMetadata, runtime)
	redactions = append(redactions, threadRedactions...)
	section := output.MessageOutput{Channel: channel, Messages: messages, Redactions: redactions, Retrieval: output.Retrieval{
		Selection:      output.Selection{Source: "thread", SelectedCount: len(result.Posts), QueryTruncated: truncatedPointer(effectiveCompleteness)},
		VisibleThreads: presentedThreads, VisiblePostCount: len(result.Posts), DeletedPostsIncluded: false,
	}}
	if unavailable {
		label := channel.ID
		if label == "" {
			label = "an unknown channel"
		}
		warning := fmt.Sprintf("warning: channel metadata is unavailable for %s\n", label)
		if display.json {
			state.queueMachineWarning(warning)
		} else {
			if err := writeAll(state.streams.err, []byte(warning)); err != nil {
				return err
			}
		}
	}
	if !complete {
		root := presentedThreads.FailedRootIDs[0]
		warning := fmt.Sprintf("warning: thread %s could only be partially hydrated\n", root)
		if display.json {
			state.queueMachineWarning(warning)
		} else {
			if err := writeAll(state.streams.err, []byte(warning)); err != nil {
				return err
			}
		}
	}
	envelope, err := output.NewThreadEnvelope(section, machineCompleteness(effectiveCompleteness))
	if err != nil {
		return readFailure(err)
	}
	return state.renderRead([]output.MessageOutput{section}, envelope, display)
}
