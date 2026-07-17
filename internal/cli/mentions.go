package cli

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/ardasevinc/mattermost-cli/v2/internal/mattermost"
	"github.com/ardasevinc/mattermost-cli/v2/internal/output"
	"github.com/ardasevinc/mattermost-cli/v2/internal/retrieval"
)

type mentionsFlags struct {
	team, limit, since, channel string
}

func newMentionsCommand(state *rootState) *cobra.Command {
	flags := new(mentionsFlags)
	command := &cobra.Command{
		Use: "mentions", Short: "Find mentions within one selected team", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return runMentions(cmd, state, *flags) },
	}
	command.Flags().StringVar(&flags.team, "team", "", "team name (auto-detected for one team)")
	command.Flags().StringVarP(&flags.limit, "limit", "l", "50", "maximum seed results")
	command.Flags().StringVarP(&flags.since, "since", "s", "", "time range such as 24h, 7d, 1w, or 2m")
	command.Flags().StringVar(&flags.channel, "channel", "", "scope mentions to a channel name")
	return command
}

func runMentions(cmd *cobra.Command, state *rootState, flags mentionsFlags) error {
	limit, err := positiveInteger(flags.limit)
	if err != nil {
		return err
	}
	var since *int64
	if flags.since != "" {
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
	team, err := runtime.Teams.Resolve(cmd.Context(), me.ID, flags.team)
	if err != nil {
		return readFailure(err)
	}
	var channelName, channelID *string
	if flags.channel != "" {
		channel, channelErr := runtime.Channels.ByName(cmd.Context(), team.ID, flags.channel)
		if channelErr != nil {
			return readFailure(channelErr)
		}
		channelName, channelID = &channel.Name, &channel.ID
	}
	result, err := retrieval.Mentions(cmd.Context(), runtime.Posts, team.ID, retrieval.MentionsOptions{
		Username: me.Username, Aliases: runtime.Config.MentionNames, Channel: channelName, ChannelID: channelID, Since: since, Limit: limit,
	})
	if err != nil {
		return readFailure(err)
	}
	if len(result.Posts) == 0 && result.Completeness != retrieval.CompletenessComplete {
		return readError("Mattermost could not confirm an empty mentions result")
	}

	groups, order := groupSearchPosts(result.Posts)
	type hydratedMentionGroup struct {
		channelID string
		seeds     []mattermost.Post
		hydrated  retrieval.HydrationResult
	}
	hydratedGroups := make([]hydratedMentionGroup, 0, len(order))
	allPosts := make([]mattermost.Post, 0, len(result.Posts))
	for _, channelID := range order {
		seeds := groups[channelID]
		hydrated, hydrateErr := retrieval.HydrateVisibleThreads(cmd.Context(), runtime.Posts, seeds, display.threads)
		if hydrateErr != nil {
			return readFailure(hydrateErr)
		}
		hydratedGroups = append(hydratedGroups, hydratedMentionGroup{channelID: channelID, seeds: seeds, hydrated: hydrated})
		allPosts = append(allPosts, hydrated.Posts...)
	}
	users, err := loadReadUsers(cmd.Context(), runtime, allPosts)
	if err != nil {
		return readFailure(err)
	}
	sections := make([]output.MessageOutput, 0, len(order))
	for _, group := range hydratedGroups {
		messages, redactions, normalizeErr := normalizeReadPostsWithUsers(runtime, group.hydrated.Posts, users, me.ID)
		if normalizeErr != nil {
			return readFailure(normalizeErr)
		}
		if display.threads {
			messages = output.GroupIntoThreads(messages)
		}
		channel, channelRedactions, unavailable, channelErr := resolvedReadChannel(cmd.Context(), runtime, group.channelID, me.ID, readChannelBinding{
			selectedTeamID: team.ID, requireGroupMembership: true,
		})
		if channelErr != nil {
			return readFailure(channelErr)
		}
		redactions = append(channelRedactions, redactions...)
		threads, threadRedactions := processedVisibleThreads(group.hydrated.VisibleThreads, runtime)
		redactions = append(redactions, threadRedactions...)
		requestedLimit := limit
		var sinceText *string
		if since != nil {
			value := time.UnixMilli(*since).UTC().Format("2006-01-02T15:04:05.000Z")
			sinceText = &value
		}
		sections = append(sections, output.MessageOutput{Channel: channel, Messages: messages, Redactions: redactions, Retrieval: output.Retrieval{
			Selection:      output.Selection{Source: "mentions", SelectedCount: len(group.seeds), RequestedLimit: &requestedLimit, Since: sinceText, QueryTruncated: truncatedPointer(result.Completeness)},
			VisibleThreads: threads, VisiblePostCount: len(group.hydrated.Posts), DeletedPostsIncluded: false,
		}})
		if unavailable {
			label := channel.ID
			if label == "" {
				label = "an unknown channel"
			}
			if err := searchWarning(state, display.json, fmt.Sprintf("warning: channel metadata is unavailable for %s\n", label)); err != nil {
				return err
			}
		}
		for _, rootID := range threads.FailedRootIDs {
			if err := searchWarning(state, display.json, fmt.Sprintf("warning: thread %s could only be partially hydrated\n", rootID)); err != nil {
				return err
			}
		}
	}
	envelope, err := output.NewMentionsEnvelope(sections, machineCompleteness(result.Completeness))
	if err != nil {
		return internalFailure(err)
	}
	return state.renderRead(sections, envelope, display)
}
