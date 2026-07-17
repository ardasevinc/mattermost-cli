package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ardasevinc/mattermost-cli/v2/internal/mattermost"
	"github.com/ardasevinc/mattermost-cli/v2/internal/output"
	"github.com/ardasevinc/mattermost-cli/v2/internal/retrieval"
)

type searchFlags struct {
	team, limit string
}

func newSearchCommand(state *rootState) *cobra.Command {
	flags := new(searchFlags)
	command := &cobra.Command{
		Use: "search <query>", Short: "Search messages within one selected team", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error { return runSearch(cmd, state, args[0], *flags) },
	}
	command.Flags().StringVar(&flags.team, "team", "", "team name (auto-detected for one team)")
	command.Flags().StringVarP(&flags.limit, "limit", "l", "50", "maximum seed results")
	return command
}

func runSearch(cmd *cobra.Command, state *rootState, rawQuery string, flags searchFlags) error {
	query := strings.TrimSpace(rawQuery)
	if query == "" {
		return invalidFailure("search query cannot be empty")
	}
	limit, err := positiveInteger(flags.limit)
	if err != nil {
		return err
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
	result, err := retrieval.Search(cmd.Context(), runtime.Posts, team.ID, query, retrieval.SearchOptions{
		Limit:  limit,
		Accept: func(post mattermost.Post) bool { return post.DeleteAt == 0 },
	})
	if err != nil {
		return readFailure(err)
	}
	if len(result.Posts) == 0 && result.Completeness == retrieval.CompletenessUnknown {
		return readError("Mattermost could not confirm an empty search result")
	}

	groups, order := groupSearchPosts(result.Posts)
	type hydratedSearchGroup struct {
		channelID string
		seeds     []mattermost.Post
		hydrated  retrieval.HydrationResult
	}
	hydratedGroups := make([]hydratedSearchGroup, 0, len(order))
	allPosts := make([]mattermost.Post, 0, len(result.Posts))
	for _, channelID := range order {
		seeds := groups[channelID]
		hydrated, hydrateErr := retrieval.HydrateVisibleThreads(cmd.Context(), runtime.Posts, seeds, display.threads)
		if hydrateErr != nil {
			return readFailure(hydrateErr)
		}
		hydratedGroups = append(hydratedGroups, hydratedSearchGroup{channelID: channelID, seeds: seeds, hydrated: hydrated})
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
		sections = append(sections, output.MessageOutput{Channel: channel, Messages: messages, Redactions: redactions, Retrieval: output.Retrieval{
			Selection:      output.Selection{Source: "search", SelectedCount: len(group.seeds), RequestedLimit: &requestedLimit, QueryTruncated: truncatedPointer(result.Completeness)},
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
	envelope, err := output.NewSearchEnvelope(sections, machineCompleteness(result.Completeness))
	if err != nil {
		return internalFailure(err)
	}
	return state.renderRead(sections, envelope, display)
}

func groupSearchPosts(posts []mattermost.Post) (map[string][]mattermost.Post, []string) {
	groups := make(map[string][]mattermost.Post)
	order := make([]string, 0)
	for _, post := range posts {
		if _, exists := groups[post.ChannelID]; !exists {
			order = append(order, post.ChannelID)
		}
		groups[post.ChannelID] = append(groups[post.ChannelID], post)
	}
	return groups, order
}

func searchWarning(state *rootState, machine bool, warning string) error {
	if machine {
		state.queueMachineWarning(warning)
		return nil
	}
	return writeAll(state.streams.err, []byte(warning))
}
