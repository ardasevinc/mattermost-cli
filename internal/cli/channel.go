package cli

import (
	"regexp"
	"strconv"
	"time"

	"github.com/spf13/cobra"

	"github.com/ardasevinc/mattermost-cli/internal/cursor"
	"github.com/ardasevinc/mattermost-cli/internal/output"
	"github.com/ardasevinc/mattermost-cli/internal/retrieval"
)

type channelFlags struct {
	team, limit, since, cursor string
}

func newChannelCommand(state *rootState) *cobra.Command {
	flags := new(channelFlags)
	command := &cobra.Command{
		Use: "channel <name>", Short: "Fetch messages from a channel by name", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error { return runChannel(cmd, state, args[0], *flags) },
	}
	command.Flags().StringVar(&flags.team, "team", "", "team name (auto-detected for one team)")
	command.Flags().StringVarP(&flags.limit, "limit", "l", "50", "maximum seed messages")
	command.Flags().StringVarP(&flags.since, "since", "s", "7d", "time range such as 24h, 7d, 1w, or 2m")
	command.Flags().StringVar(&flags.cursor, "cursor", "", "resume deterministic channel history")
	return command
}

func runChannel(cmd *cobra.Command, state *rootState, name string, flags channelFlags) error {
	limit, err := positiveInteger(flags.limit)
	if err != nil {
		return err
	}
	display, err := state.readDisplay(cmd)
	if err != nil {
		return err
	}
	var resume *cursor.ChannelHistory
	if flags.cursor != "" {
		decoded, decodeErr := cursor.DecodeChannelHistory(flags.cursor)
		if decodeErr != nil {
			return invalidFailure("invalid channel cursor")
		}
		resume = &decoded
		if cmd.Flags().Changed("since") {
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
	channel, err := runtime.Channels.ByName(cmd.Context(), team.ID, name)
	if err != nil {
		return readFailure(err)
	}
	if resume != nil && resume.ChannelID != channel.ID {
		return invalidFailure("cursor does not match the selected channel")
	}
	options := retrieval.ChannelHistoryOptions{Limit: limit, Since: since}
	if resume != nil {
		options.Boundary = &retrieval.Boundary{CreateAt: resume.Boundary.CreateAt, ID: resume.Boundary.ID}
		options.SafeBeforePostID = resume.SafeBeforePostID
	}
	page, err := retrieval.ChannelHistory(cmd.Context(), runtime.Posts, channel.ID, options)
	if err != nil {
		return readFailure(err)
	}
	if len(page.Posts) == 0 && page.Completeness == retrieval.CompletenessUnknown && resume == nil {
		return readError("Mattermost could not confirm an empty channel history")
	}

	nextCursor := ""
	if len(page.Posts) > 0 && page.Completeness != retrieval.CompletenessComplete {
		boundary := page.Posts[len(page.Posts)-1]
		safeBefore := ""
		for index := len(page.Posts) - 1; index >= 0; index-- {
			if page.Posts[index].CreateAt > boundary.CreateAt {
				safeBefore = page.Posts[index].ID
				break
			}
		}
		if safeBefore == "" && page.SafeBeforeValid && resume != nil {
			safeBefore = resume.SafeBeforePostID
		}
		nextCursor, err = cursor.EncodeChannelHistory(cursor.ChannelHistory{
			Version: 1, Scope: "channel", ChannelID: channel.ID,
			Boundary: cursor.Boundary{CreateAt: boundary.CreateAt, ID: boundary.ID}, Since: since, SafeBeforePostID: safeBefore,
		})
		if err != nil {
			return readFailure(err)
		}
	} else if len(page.Posts) == 0 && page.Completeness == retrieval.CompletenessUnknown && resume != nil {
		nextCursor = flags.cursor
	}

	hydrated, err := retrieval.HydrateVisibleThreads(cmd.Context(), runtime.Posts, page.Posts, display.threads)
	if err != nil {
		return readFailure(err)
	}
	messages, redactions, err := normalizeReadPosts(cmd.Context(), runtime, hydrated.Posts, me.ID)
	if err != nil {
		return readFailure(err)
	}
	if display.threads {
		messages = output.GroupIntoThreads(messages)
	}
	presentedChannel, channelRedactions := processedChannel(channel, runtime)
	redactions = append(channelRedactions, redactions...)
	threadsMetadata, threadRedactions := processedVisibleThreads(hydrated.VisibleThreads, runtime)
	redactions = append(redactions, threadRedactions...)
	selectedCount := len(page.Posts)
	requestedLimit := limit
	var sinceText *string
	if since != nil {
		value := time.UnixMilli(*since).UTC().Format("2006-01-02T15:04:05.000Z")
		sinceText = &value
	}
	inputCursor, next := stringPointer(flags.cursor), stringPointer(nextCursor)
	section := output.MessageOutput{Channel: presentedChannel, Messages: messages, Redactions: redactions, Retrieval: output.Retrieval{
		Selection: output.Selection{Source: "recent", SelectedCount: selectedCount, RequestedLimit: &requestedLimit, Since: sinceText,
			QueryTruncated: truncatedPointer(page.Completeness), InputCursor: inputCursor, NextCursor: next},
		VisibleThreads: threadsMetadata, VisiblePostCount: len(hydrated.Posts), DeletedPostsIncluded: false,
	}}
	envelope, err := output.NewChannelEnvelope(section, machineCompleteness(page.Completeness))
	if err != nil {
		return internalFailure(err)
	}
	humanOutputs := []output.MessageOutput{section}
	if len(page.Posts) == 0 && page.Completeness == retrieval.CompletenessComplete {
		humanOutputs = nil
	}
	return state.renderRead(humanOutputs, envelope, display)
}

var durationPattern = regexp.MustCompile(`^([0-9]+)([hHdDwWmM])$`)

func positiveInteger(value string) (int, error) {
	if value == "" || value[0] == '0' {
		return 0, invalidFailure("--limit must be a positive number")
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return 0, invalidFailure("--limit must be a positive number")
		}
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed <= 0 || parsed > 9_007_199_254_740_991 {
		return 0, invalidFailure("--limit must be a positive number")
	}
	return int(parsed), nil
}

func durationBoundary(value string, now time.Time) (int64, error) {
	match := durationPattern.FindStringSubmatch(value)
	if match == nil {
		return 0, invalidFailure(`--since must use a duration such as "24h", "7d", "1w", or "2m"`)
	}
	amount, err := strconv.ParseUint(match[1], 10, 63)
	if err != nil {
		return 0, invalidFailure("--since duration is too large")
	}
	hours := uint64(1)
	switch match[2][0] | 0x20 {
	case 'd':
		hours = 24
	case 'w':
		hours = 24 * 7
	case 'm':
		hours = 24 * 30
	}
	if amount > uint64(now.UnixMilli())/(hours*uint64(time.Hour/time.Millisecond)) {
		return 0, invalidFailure("--since duration is too large")
	}
	return now.UnixMilli() - int64(amount*hours*uint64(time.Hour/time.Millisecond)), nil
}

func stringPointer(value string) *string {
	if value == "" {
		return nil
	}
	copy := value
	return &copy
}
