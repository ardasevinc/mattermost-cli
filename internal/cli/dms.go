package cli

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/ardasevinc/mattermost-cli/v2/internal/api"
	"github.com/ardasevinc/mattermost-cli/v2/internal/cursor"
	"github.com/ardasevinc/mattermost-cli/v2/internal/mattermost"
	"github.com/ardasevinc/mattermost-cli/v2/internal/normalization"
	"github.com/ardasevinc/mattermost-cli/v2/internal/output"
	"github.com/ardasevinc/mattermost-cli/v2/internal/retrieval"
)

type dmsFlags struct {
	users                         []string
	limit, since, channel, cursor string
}

func newDMsCommand(state *rootState) *cobra.Command {
	flags := new(dmsFlags)
	command := &cobra.Command{Use: "dms", Short: "Fetch direct messages", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return runDMs(cmd, state, *flags) }}
	command.Flags().StringSliceVarP(&flags.users, "user", "u", nil, "filter by username (repeatable or comma-separated)")
	command.Flags().StringVarP(&flags.limit, "limit", "l", "50", "maximum total seed messages across matched DMs")
	command.Flags().StringVarP(&flags.since, "since", "s", "7d", "time range such as 24h, 7d, 1w, or 2m")
	command.Flags().StringVarP(&flags.channel, "channel", "c", "", "specific direct-message channel ID")
	command.Flags().StringVar(&flags.cursor, "cursor", "", "resume deterministic direct-message history")
	return command
}

func runDMs(cmd *cobra.Command, state *rootState, flags dmsFlags) error {
	if flagChanged(cmd, "user") {
		if len(flags.users) == 0 {
			return invalidFailure("--user cannot be empty")
		}
		for _, username := range flags.users {
			if strings.TrimSpace(username) == "" {
				return invalidFailure("--user cannot be empty")
			}
		}
	}
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
	if flags.channel != "" && len(flags.users) != 0 {
		return invalidFailure("--channel cannot be combined with --user")
	}
	var resume *cursor.ChannelHistory
	if flags.cursor != "" {
		decoded, decodeErr := cursor.DecodeChannelHistory(flags.cursor)
		if decodeErr != nil {
			return invalidFailure("invalid direct-message cursor")
		}
		resume = &decoded
		if flags.channel == "" {
			return invalidFailure("a cursor requires --channel for direct-message history")
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

	channels, err := selectDMChannels(cmd, state, runtime, me, flags, display.json)
	if err != nil {
		return err
	}
	if resume != nil && (len(channels) != 1 || resume.ChannelID != channels[0].ID) {
		return invalidFailure("cursor does not match the selected channel")
	}
	if len(channels) == 0 {
		envelope, envelopeErr := output.NewDMSEnvelope(nil, output.MachineComplete)
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
	options := retrieval.DMHistoryOptions{Limit: limit, Since: since}
	if resume != nil {
		options.Boundary = &retrieval.Boundary{CreateAt: resume.Boundary.CreateAt, ID: resume.Boundary.ID}
		options.SafeBeforePostID = resume.SafeBeforePostID
	}
	result, err := retrieval.DMHistory(cmd.Context(), runtime.Posts, ids, options)
	if err != nil {
		return readFailure(err)
	}
	if len(result.Posts) == 0 && result.Completeness == retrieval.CompletenessUnknown && resume == nil {
		return readError("Mattermost could not confirm an empty direct-message history")
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
	userIDs := make([]string, 0, len(hydrated))
	for _, group := range hydrated {
		userIDs = append(userIDs, otherDMUserID(byID[group.channelID], me.ID))
	}
	if len(result.Posts) == 0 && resume != nil && result.Completeness == retrieval.CompletenessUnknown {
		userIDs = append(userIDs, otherDMUserID(channels[0], me.ID))
	}
	users, err := loadReadUsersAndIDs(cmd, runtime, allPosts, userIDs)
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
		partner := users[otherDMUserID(byID[group.channelID], me.ID)]
		channel, channelRedactions := presentedDMChannel(byID[group.channelID], partner, runtime)
		redactions = append(channelRedactions, redactions...)
		threads, threadRedactions := processedVisibleThreads(group.hydrated.VisibleThreads, runtime)
		redactions = append(redactions, threadRedactions...)
		requestedLimit := limit
		sinceText := millisecondTimestamp(since)
		sections = append(sections, output.MessageOutput{Channel: channel, Messages: messages, Redactions: redactions, Retrieval: output.Retrieval{
			Selection: output.Selection{Source: "recent", SelectedCount: len(group.seeds), RequestedLimit: &requestedLimit, Since: sinceText,
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
		partner := users[otherDMUserID(channels[0], me.ID)]
		channel, redactions := presentedDMChannel(channels[0], partner, runtime)
		requestedLimit := limit
		sinceText := millisecondTimestamp(since)
		sections = append(sections, output.MessageOutput{Channel: channel, Redactions: redactions, Retrieval: output.Retrieval{
			Selection:      output.Selection{Source: "recent", RequestedLimit: &requestedLimit, Since: sinceText, InputCursor: stringPointer(flags.cursor), NextCursor: stringPointer(nextCursor)},
			VisibleThreads: output.VisibleThreads{Status: map[bool]string{true: "complete", false: "not_requested"}[display.threads], FailedRootIDs: []string{}},
		}})
	}
	envelope, err := output.NewDMSEnvelope(sections, machineCompleteness(result.Completeness))
	if err != nil {
		return internalFailure(err)
	}
	return state.renderRead(sections, envelope, display)
}

func selectDMChannels(cmd *cobra.Command, state *rootState, runtime *Runtime, me mattermost.User, flags dmsFlags, machine bool) ([]mattermost.Channel, error) {
	if flags.channel != "" {
		channel, err := runtime.Channels.ByID(cmd.Context(), flags.channel)
		if err != nil {
			return nil, readFailure(err)
		}
		if channel.Type != "D" {
			return nil, invalidFailure("selected channel is not a direct-message channel")
		}
		if otherDMUserID(channel, me.ID) == "" {
			return nil, readFailure(mattermost.ErrInvalidChannelResponse)
		}
		return []mattermost.Channel{channel}, nil
	}
	all, err := runtime.Channels.DirectList(cmd.Context(), me.ID)
	if err != nil {
		return nil, readFailure(err)
	}
	direct := all
	if len(flags.users) == 0 {
		return direct, nil
	}
	byPartner := make(map[string]mattermost.Channel, len(direct))
	for _, channel := range direct {
		byPartner[otherDMUserID(channel, me.ID)] = channel
	}
	selected := make(map[string]mattermost.Channel)
	seenNames := make(map[string]struct{})
	for _, username := range flags.users {
		key := strings.ToLower(username)
		if _, duplicate := seenNames[key]; duplicate {
			continue
		}
		seenNames[key] = struct{}{}
		user, lookupErr := runtime.Users.ByUsername(cmd.Context(), username)
		if lookupErr != nil {
			var remote *api.APIError
			if errors.As(lookupErr, &remote) && remote.Status == 404 {
				if err := dmWarning(state, machine, fmt.Sprintf("warning: user @%s was not found\n", safeWarningLabel(username, runtime))); err != nil {
					return nil, err
				}
				continue
			}
			return nil, readFailure(lookupErr)
		}
		channel, exists := byPartner[user.ID]
		if !exists {
			if err := dmWarning(state, machine, fmt.Sprintf("warning: no direct-message channel exists with @%s\n", safeWarningLabel(username, runtime))); err != nil {
				return nil, err
			}
			continue
		}
		selected[channel.ID] = channel
	}
	result := make([]mattermost.Channel, 0, len(selected))
	for _, channel := range selected {
		result = append(result, channel)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result, nil
}

func otherDMUserID(channel mattermost.Channel, me string) string {
	parts := strings.Split(channel.Name, "__")
	if channel.Type != "D" || len(parts) != 2 {
		return ""
	}
	if parts[0] == me {
		return parts[1]
	}
	if parts[1] == me {
		return parts[0]
	}
	return ""
}

func loadReadUsersAndIDs(cmd *cobra.Command, runtime *Runtime, posts []mattermost.Post, extra []string) (map[string]mattermost.User, error) {
	ids := make([]string, 0, len(extra)+len(posts))
	ids = append(ids, extra...)
	ids = append(ids, normalization.PostUserIDs(posts)...)
	users := make(map[string]mattermost.User)
	seen := make(map[string]struct{})
	unique := make([]string, 0, len(ids))
	for _, id := range ids {
		if id != "" {
			if _, ok := seen[id]; !ok {
				seen[id] = struct{}{}
				unique = append(unique, id)
			}
		}
	}
	for start := 0; start < len(unique); start += 200 {
		end := start + 200
		if end > len(unique) {
			end = len(unique)
		}
		batch, err := runtime.Users.ByIDs(cmd.Context(), unique[start:end])
		if err != nil {
			return nil, err
		}
		for _, user := range batch {
			users[user.ID] = user
		}
	}
	return users, nil
}

func presentedDMChannel(raw mattermost.Channel, partner mattermost.User, runtime *Runtime) (output.Channel, []output.Redaction) {
	id, redactions := processLabel(raw.ID, "channel.id", runtime)
	name, nameRedactions := processLabel(partner.Username, "channel.dmUsername", runtime)
	return output.Channel{ID: id, Type: "dm", Name: "@" + name, MetadataStatus: "resolved"}, append(redactions, nameRedactions...)
}

func safeWarningLabel(value string, runtime *Runtime) string {
	label, _ := processLabel(value, "warning", runtime)
	return label
}
func millisecondTimestamp(value *int64) *string {
	if value == nil {
		return nil
	}
	formatted := time.UnixMilli(*value).UTC().Format("2006-01-02T15:04:05.000Z")
	return &formatted
}
func dmWarning(state *rootState, machine bool, warning string) error {
	return searchWarning(state, machine, warning)
}
