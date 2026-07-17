package cli

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/ardasevinc/mattermost-cli/v2/internal/cursor"
	"github.com/ardasevinc/mattermost-cli/v2/internal/mattermost"
	"github.com/ardasevinc/mattermost-cli/v2/internal/output"
	"github.com/ardasevinc/mattermost-cli/v2/internal/retrieval"
)

type groupDMsFlags struct{ limit, since, channel, cursor string }

func newGroupDMsCommand(state *rootState) *cobra.Command {
	flags := new(groupDMsFlags)
	command := &cobra.Command{Use: "group-dms", Short: "Fetch group direct messages", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return runGroupDMs(cmd, state, *flags) }}
	command.Flags().StringVarP(&flags.limit, "limit", "l", "50", "maximum total seed messages across matched group DMs")
	command.Flags().StringVarP(&flags.since, "since", "s", "7d", "time range such as 24h, 7d, 1w, or 2m")
	command.Flags().StringVarP(&flags.channel, "channel", "c", "", "specific group DM channel ID")
	command.Flags().StringVar(&flags.cursor, "cursor", "", "resume deterministic group-DM history")
	return command
}

func runGroupDMs(cmd *cobra.Command, state *rootState, flags groupDMsFlags) error {
	if flagChanged(cmd, "channel") && strings.TrimSpace(flags.channel) == "" {
		return invalidFailure("--channel cannot be empty")
	}
	if flagChanged(cmd, "cursor") && strings.TrimSpace(flags.cursor) == "" {
		return invalidFailure("--cursor cannot be empty")
	}
	limit, err := positiveInteger(flags.limit)
	if err != nil {
		return err
	}
	var resume *cursor.ChannelHistory
	if flags.cursor != "" {
		decoded, decodeErr := cursor.DecodeChannelHistory(flags.cursor)
		if decodeErr != nil {
			return invalidFailure("invalid group-DM cursor")
		}
		resume = &decoded
		if flags.channel == "" {
			return invalidFailure("a cursor requires --channel for group-DM history")
		}
		if decoded.ChannelID != flags.channel {
			return invalidFailure("cursor does not match the selected channel")
		}
		if flagChanged(cmd, "since") {
			return invalidFailure("a cursor cannot be combined with --since")
		}
	}
	var since *int64
	if resume != nil {
		since = resume.Since
	} else {
		value, durationErr := durationBoundary(flags.since, time.Now())
		if durationErr != nil {
			return durationErr
		}
		since = &value
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
	channels, err := selectGroupDMChannels(cmd, runtime, me, flags)
	if err != nil {
		return err
	}
	if resume != nil && (len(channels) != 1 || resume.ChannelID != channels[0].ID) {
		return invalidFailure("cursor does not match the selected channel")
	}
	if len(channels) == 0 {
		envelope, envelopeErr := output.NewGroupDMSEnvelope(nil, output.MachineComplete)
		if envelopeErr != nil {
			return internalFailure(envelopeErr)
		}
		return state.renderRead(nil, envelope, display)
	}
	ids := make([]string, len(channels))
	byID := make(map[string]mattermost.Channel, len(channels))
	for index, channel := range channels {
		ids[index], byID[channel.ID] = channel.ID, channel
	}
	options := retrieval.GroupDMHistoryOptions{Limit: limit, Since: since}
	if resume != nil {
		options.Boundary = &retrieval.Boundary{CreateAt: resume.Boundary.CreateAt, ID: resume.Boundary.ID}
		options.SafeBeforePostID = resume.SafeBeforePostID
	}
	result, err := retrieval.GroupDMHistory(cmd.Context(), runtime.Posts, ids, options)
	if err != nil {
		return readFailure(err)
	}
	if len(result.Posts) == 0 && result.Completeness == retrieval.CompletenessUnknown && resume == nil {
		return readError("Mattermost could not confirm an empty group direct-message history")
	}
	nextCursor := ""
	if len(result.Posts) > 0 && result.Completeness != retrieval.CompletenessComplete && flags.channel != "" {
		boundary := result.Posts[len(result.Posts)-1]
		safeBefore := ""
		for index := len(result.Posts) - 1; index >= 0; index-- {
			if result.Posts[index].CreateAt > boundary.CreateAt {
				safeBefore = result.Posts[index].ID
				break
			}
		}
		if safeBefore == "" && result.SafeBeforeValid && resume != nil {
			safeBefore = resume.SafeBeforePostID
		}
		nextCursor, err = cursor.EncodeChannelHistory(cursor.ChannelHistory{Version: 1, Scope: "channel", ChannelID: channels[0].ID,
			Boundary: cursor.Boundary{CreateAt: boundary.CreateAt, ID: boundary.ID}, Since: since, SafeBeforePostID: safeBefore})
		if err != nil {
			return readFailure(err)
		}
	} else if len(result.Posts) == 0 && result.Completeness == retrieval.CompletenessUnknown && resume != nil {
		nextCursor = flags.cursor
	}

	groups, order := groupSearchPosts(result.Posts)
	type hydratedGroup struct {
		channelID string
		seeds     []mattermost.Post
		hydrated  retrieval.HydrationResult
	}
	hydrated := make([]hydratedGroup, 0, len(order))
	allPosts := make([]mattermost.Post, 0, len(result.Posts))
	for _, channelID := range order {
		value, hydrateErr := retrieval.HydrateVisibleThreads(cmd.Context(), runtime.Posts, groups[channelID], display.threads)
		if hydrateErr != nil {
			return readFailure(hydrateErr)
		}
		hydrated = append(hydrated, hydratedGroup{channelID, groups[channelID], value})
		allPosts = append(allPosts, value.Posts...)
	}
	users, err := loadReadUsersAndIDs(cmd, runtime, allPosts, nil)
	if err != nil {
		return readFailure(err)
	}
	sections := make([]output.MessageOutput, 0, len(hydrated))
	for _, group := range hydrated {
		messages, redactions, normalizeErr := normalizeReadPostsWithUsers(runtime, group.hydrated.Posts, users, me.ID)
		if normalizeErr != nil {
			return readFailure(normalizeErr)
		}
		if display.threads {
			messages = output.GroupIntoThreads(messages)
		}
		channel, channelRedactions := processedChannel(byID[group.channelID], runtime)
		redactions = append(channelRedactions, redactions...)
		threads, threadRedactions := processedVisibleThreads(group.hydrated.VisibleThreads, runtime)
		redactions = append(redactions, threadRedactions...)
		requestedLimit := limit
		sections = append(sections, output.MessageOutput{Channel: channel, Messages: messages, Redactions: redactions, Retrieval: output.Retrieval{
			Selection: output.Selection{Source: "recent", SelectedCount: len(group.seeds), RequestedLimit: &requestedLimit, Since: millisecondTimestamp(since),
				QueryTruncated: truncatedPointer(result.Completeness), InputCursor: stringPointer(flags.cursor), NextCursor: stringPointer(nextCursor)},
			VisibleThreads: threads, VisiblePostCount: len(group.hydrated.Posts), DeletedPostsIncluded: false,
		}})
		for _, rootID := range threads.FailedRootIDs {
			if err := searchWarning(state, display.json, fmt.Sprintf("warning: thread %s could only be partially hydrated\n", rootID)); err != nil {
				return err
			}
		}
	}
	if len(result.Posts) == 0 && resume != nil && result.Completeness == retrieval.CompletenessUnknown {
		channel, redactions := processedChannel(channels[0], runtime)
		requestedLimit := limit
		sections = append(sections, output.MessageOutput{Channel: channel, Redactions: redactions, Retrieval: output.Retrieval{
			Selection:      output.Selection{Source: "recent", RequestedLimit: &requestedLimit, Since: millisecondTimestamp(since), InputCursor: stringPointer(flags.cursor), NextCursor: stringPointer(nextCursor)},
			VisibleThreads: output.VisibleThreads{Status: map[bool]string{true: "complete", false: "not_requested"}[display.threads], FailedRootIDs: []string{}},
		}})
	}
	envelope, err := output.NewGroupDMSEnvelope(sections, machineCompleteness(result.Completeness))
	if err != nil {
		return internalFailure(err)
	}
	return state.renderRead(sections, envelope, display)
}

func selectGroupDMChannels(cmd *cobra.Command, runtime *Runtime, me mattermost.User, flags groupDMsFlags) ([]mattermost.Channel, error) {
	if flags.channel != "" {
		channel, err := runtime.Channels.ByID(cmd.Context(), flags.channel)
		if err != nil {
			return nil, readFailure(err)
		}
		if channel.Type != "G" {
			return nil, invalidFailure("selected channel is not a group direct-message channel")
		}
		if _, err := runtime.Channels.Member(cmd.Context(), channel.ID, me.ID); err != nil {
			return nil, readFailure(err)
		}
		return []mattermost.Channel{channel}, nil
	}
	channels, err := runtime.Channels.GroupList(cmd.Context(), me.ID)
	if err != nil {
		return nil, readFailure(err)
	}
	return channels, nil
}
