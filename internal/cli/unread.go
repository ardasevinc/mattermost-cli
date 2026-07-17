package cli

import (
	"bytes"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/ardasevinc/mattermost-cli/v2/internal/mattermost"
	"github.com/ardasevinc/mattermost-cli/v2/internal/output"
	"github.com/ardasevinc/mattermost-cli/v2/internal/retrieval"
)

type unreadFlags struct {
	team string
	peek string
}

func newUnreadCommand(state *rootState) *cobra.Command {
	flags := new(unreadFlags)
	command := &cobra.Command{Use: "unread", Short: "Show unread channels", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return runUnread(cmd, state, *flags) }}
	command.Flags().StringVar(&flags.team, "team", "", "exact team name or display name")
	command.Flags().StringVar(&flags.peek, "peek", "", "maximum messages to preview per unread channel")
	return command
}

func runUnread(cmd *cobra.Command, state *rootState, flags unreadFlags) error {
	if flagChanged(cmd, "team") && (strings.TrimSpace(flags.team) == "" || strings.TrimSpace(flags.team) != flags.team) {
		return invalidFailure("--team must be a non-empty exact selector")
	}
	peekLimit := 0
	var err error
	if flagChanged(cmd, "peek") {
		peekLimit, err = positiveInteger(flags.peek)
		if err != nil {
			return invalidFailure("--peek must be a positive number")
		}
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
	result, err := retrieval.Unread(cmd.Context(), runtime.Users, runtime.Teams, runtime.Channels, runtime.Posts, retrieval.UnreadOptions{TeamSelector: flags.team, PeekLimit: peekLimit})
	if err != nil {
		return readFailure(err)
	}

	allPosts := make([]mattermost.Post, 0)
	peerIDs := make([]string, 0)
	for _, entry := range result.Entries {
		allPosts = append(allPosts, entry.Peek...)
		if entry.Channel.Type == "D" {
			peerID := otherDMUserID(entry.Channel, result.User.ID)
			if peerID == "" {
				return readError("Mattermost returned an invalid direct channel identity")
			}
			peerIDs = append(peerIDs, peerID)
		}
	}
	sort.Strings(peerIDs)
	users, err := loadReadUsersAndIDs(cmd, runtime, allPosts, peerIDs)
	if err != nil {
		return readFailure(err)
	}

	raw := make([]output.RawUnreadItem, len(result.Entries))
	sections := make([]output.MessageOutput, 0, len(result.Entries))
	for i, entry := range result.Entries {
		channel := entry.Channel
		rawChannel := output.RawChannel{ID: channel.ID, Type: channel.Type, Name: channel.Name, DisplayName: channel.DisplayName, TeamID: channel.TeamID, LastPostAt: channel.LastPostAt, TotalMsgCount: channel.TotalMsgCount}
		var presented output.Channel
		var channelRedactions []output.Redaction
		switch channel.Type {
		case "D":
			peer := users[otherDMUserID(channel, result.User.ID)]
			if peer.ID == "" {
				return readError("Mattermost returned incomplete direct-message identity metadata")
			}
			rawChannel.DirectUsername = peer.Username
			presented, channelRedactions = presentedDMChannel(channel, peer, runtime)
		case "G":
			presented, channelRedactions = processedChannel(channel, runtime)
		case "O", "P":
			if channel.TeamID != result.Team.ID {
				return readError("Mattermost returned a channel outside the selected team")
			}
			team := output.RawTeam{ID: result.Team.ID, Name: result.Team.Name, DisplayName: result.Team.DisplayName, Type: result.Team.Type}
			rawChannel.Team = &team
			presented, channelRedactions = processedChannel(channel, runtime)
		default:
			return readError("Mattermost returned an unsupported unread channel type")
		}
		raw[i] = output.RawUnreadItem{Channel: rawChannel, UnreadCount: entry.UnreadCount, MentionCount: entry.MentionCount, LastViewedAt: entry.LastViewedAt}
		if peekLimit == 0 {
			continue
		}
		if entry.PeekState == retrieval.CompletenessUnknown {
			return readError("Mattermost could not prove a complete unread preview")
		}
		messages, redactions, normalizeErr := normalizeReadPostsWithUsers(runtime, entry.Peek, users, result.User.ID)
		if normalizeErr != nil {
			return readFailure(normalizeErr)
		}
		redactions = append(channelRedactions, redactions...)
		limit := peekLimit
		sections = append(sections, output.MessageOutput{Channel: presented, Messages: messages, Redactions: redactions, Retrieval: output.Retrieval{
			Selection:      output.Selection{Source: "unread", SelectedCount: len(entry.Peek), RequestedLimit: &limit, Since: unreadSince(entry.LastViewedAt), QueryTruncated: truncatedPointer(entry.PeekState)},
			VisibleThreads: output.VisibleThreads{Status: "not_requested", FailedRootIDs: []string{}}, VisiblePostCount: len(entry.Peek), DeletedPostsIncluded: false,
		}})
	}

	proof := output.UnreadProof{}
	if peekLimit > 0 {
		proof.PeekLimit = &peekLimit
	}
	document, err := output.NewUnreadEnvelope(raw, sections, proof, identityOptions(runtime))
	if err != nil {
		return readFailure(err)
	}
	if display.json {
		var wire bytes.Buffer
		if _, err := output.WriteMachineJSON(&wire, document); err != nil {
			return readFailure(err)
		}
		return writeAll(state.streams.out, wire.Bytes())
	}
	dates := output.NewDateFormatter(time.Now, time.Local)
	var rendered string
	if state.deps.stdoutTTY() {
		rendered, err = output.FormatUnreadPretty(document, sections, dates, output.PrettyOptions{Color: display.color, Relative: display.relative})
	} else {
		rendered, err = output.FormatUnreadMarkdown(document, sections, dates, output.MarkdownOptions{Relative: display.relative})
	}
	if err != nil {
		return readFailure(err)
	}
	return writeAll(state.streams.out, []byte(rendered+"\n"))
}

func unreadSince(milliseconds int64) *string {
	if milliseconds == 0 {
		return nil
	}
	value := time.UnixMilli(milliseconds).UTC().Format("2006-01-02T15:04:05.000Z")
	return &value
}
