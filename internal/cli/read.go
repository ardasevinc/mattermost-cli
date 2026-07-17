package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/ardasevinc/mattermost-cli/v2/internal/api"
	"github.com/ardasevinc/mattermost-cli/v2/internal/mattermost"
	"github.com/ardasevinc/mattermost-cli/v2/internal/normalization"
	"github.com/ardasevinc/mattermost-cli/v2/internal/output"
	"github.com/ardasevinc/mattermost-cli/v2/internal/presentation"
	"github.com/ardasevinc/mattermost-cli/v2/internal/retrieval"
	"github.com/spf13/cobra"
)

type readDisplayOptions struct {
	json, color, relative, threads bool
}

func (s *rootState) readDisplay(cmd *cobra.Command) (readDisplayOptions, error) {
	if flagChanged(cmd, "relative") && flagChanged(cmd, "no-relative") {
		return readDisplayOptions{}, invalidFailure("--relative and --no-relative cannot be used together")
	}
	if flagChanged(cmd, "threads") && flagChanged(cmd, "no-threads") {
		return readDisplayOptions{}, invalidFailure("--threads and --no-threads cannot be used together")
	}
	return readDisplayOptions{
		json: s.flags.json, color: !s.flags.noColor,
		relative: (s.flags.relative || detectsAgent(s.deps.lookupEnv)) && !s.flags.noRelative,
		threads:  s.flags.threads && !s.flags.noThreads,
	}, nil
}

func detectsAgent(lookup func(string) (string, bool)) bool {
	for _, name := range []string{"CLAUDECODE", "GEMINI_CLI", "CODEX_CI", "OPENCODE"} {
		if value, ok := lookup(name); ok && value == "1" {
			return true
		}
	}
	return false
}

func flagChanged(cmd *cobra.Command, name string) bool {
	flag := cmd.Flags().Lookup(name)
	return flag != nil && flag.Changed
}

func normalizeReadPosts(ctx context.Context, runtime *Runtime, posts []mattermost.Post, myUserID string) ([]output.Message, []output.Redaction, error) {
	users, err := loadReadUsers(ctx, runtime, posts)
	if err != nil {
		return nil, nil, err
	}
	return normalizeReadPostsWithUsers(runtime, posts, users, myUserID)
}

func loadReadUsers(ctx context.Context, runtime *Runtime, posts []mattermost.Post) (map[string]mattermost.User, error) {
	users := make(map[string]mattermost.User)
	ids := normalization.PostUserIDs(posts)
	if len(ids) != 0 {
		for start := 0; start < len(ids); start += 200 {
			end := start + 200
			if end > len(ids) {
				end = len(ids)
			}
			items, err := runtime.Users.ByIDs(ctx, ids[start:end])
			if err != nil {
				return nil, err
			}
			for _, user := range items {
				users[user.ID] = user
			}
		}
	}
	return users, nil
}

func normalizeReadPostsWithUsers(runtime *Runtime, posts []mattermost.Post, users map[string]mattermost.User, myUserID string) ([]output.Message, []output.Redaction, error) {
	return normalization.NormalizePosts(posts, users, myUserID, runtime.Config.URL, presentation.Options{
		Credentials: []string{runtime.Config.Token}, DisableHeuristics: !runtime.Config.Redact,
	})
}

type readChannelBinding struct {
	selectedTeamID         string
	requireGroupMembership bool
}

func processedChannel(raw mattermost.Channel, runtime *Runtime) (output.Channel, []output.Redaction) {
	options := presentation.Options{Credentials: []string{runtime.Config.Token}, DisableHeuristics: !runtime.Config.Redact}
	redactions := make([]output.Redaction, 0)
	clean := func(value, field string) string {
		result := presentation.PreprocessWithOptions(value, options)
		remapLabelPositions(result.Text, result.Redactions)
		result.Text = presentation.SanitizeLabel(result.Text)
		for _, redaction := range result.Redactions {
			redaction.Field = field
			redactions = append(redactions, redaction)
		}
		return result.Text
	}
	typeName := map[string]string{"O": "public", "P": "private", "D": "dm", "G": "group"}[raw.Type]
	if raw.Type == "G" {
		name := raw.DisplayName
		if name == "" {
			name = raw.Name
		}
		return output.Channel{ID: clean(raw.ID, "channel.id"), Type: "group", Name: clean(name, "channel.displayName"), MetadataStatus: "resolved"}, redactions
	}
	return output.Channel{
		ID: clean(raw.ID, "channel.id"), Type: typeName, Name: clean(raw.Name, "channel.name"),
		DisplayName: clean(raw.DisplayName, "channel.displayName"), MetadataStatus: "resolved",
	}, redactions
}

func resolvedReadChannel(ctx context.Context, runtime *Runtime, channelID, myUserID string, binding readChannelBinding) (output.Channel, []output.Redaction, bool, error) {
	if channelID == "" {
		channel, redactions := unavailableChannel(channelID, runtime)
		return channel, redactions, true, nil
	}
	raw, err := runtime.Channels.ByID(ctx, channelID)
	if err != nil {
		if fatalMetadataResolutionError(ctx, err) {
			return output.Channel{}, nil, false, err
		}
		channel, redactions := unavailableChannel(channelID, runtime)
		return channel, redactions, true, nil
	}
	if (raw.Type == "O" || raw.Type == "P") && binding.selectedTeamID != "" && raw.TeamID != binding.selectedTeamID {
		channel, redactions := unavailableChannel(channelID, runtime)
		return channel, redactions, true, nil
	}
	if raw.Type == "G" && binding.requireGroupMembership {
		if _, err := runtime.Channels.Member(ctx, raw.ID, myUserID); err != nil {
			if fatalMetadataResolutionError(ctx, err) {
				return output.Channel{}, nil, false, err
			}
			channel, redactions := unavailableChannel(channelID, runtime)
			return channel, redactions, true, nil
		}
	}
	if raw.Type != "D" {
		channel, redactions := processedChannel(raw, runtime)
		return channel, redactions, false, nil
	}
	parts := strings.Split(raw.Name, "__")
	if len(parts) != 2 || (parts[0] != myUserID && parts[1] != myUserID) {
		channel, redactions := unavailableChannel(channelID, runtime)
		return channel, redactions, true, nil
	}
	otherID := parts[0]
	if otherID == myUserID {
		otherID = parts[1]
	}
	other, err := runtime.Users.ByID(ctx, otherID)
	if err != nil {
		if fatalMetadataResolutionError(ctx, err) {
			return output.Channel{}, nil, false, err
		}
		channel, redactions := unavailableChannel(channelID, runtime)
		return channel, redactions, true, nil
	}
	id, redactions := processLabel(raw.ID, "channel.id", runtime)
	name, nameRedactions := processLabel(other.Username, "channel.dmUsername", runtime)
	channel := output.Channel{ID: id, Type: "dm", Name: "@" + name, MetadataStatus: "resolved"}
	redactions = append(redactions, nameRedactions...)
	return channel, redactions, false, nil
}

func fatalMetadataResolutionError(ctx context.Context, err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
		return true
	}
	var remote *api.APIError
	return errors.As(err, &remote) && remote.Status == 401
}

func unavailableChannel(channelID string, runtime *Runtime) (output.Channel, []output.Redaction) {
	id, redactions := processLabel(channelID, "channel.id", runtime)
	return output.Channel{ID: id, Type: "unknown", Name: "unknown", MetadataStatus: "unavailable"}, redactions
}

func processLabel(value, field string, runtime *Runtime) (string, []output.Redaction) {
	result := presentation.PreprocessWithOptions(value, presentation.Options{
		Credentials: []string{runtime.Config.Token}, DisableHeuristics: !runtime.Config.Redact,
	})
	remapLabelPositions(result.Text, result.Redactions)
	for index := range result.Redactions {
		result.Redactions[index].Field = field
	}
	return presentation.SanitizeLabel(result.Text), result.Redactions
}

func remapLabelPositions(text string, redactions []presentation.Redaction) {
	for index := range redactions {
		target, original, added := redactions[index].Position, 0, 0
		for _, character := range text {
			width := len(utf16.Encode([]rune{character}))
			if original+width > target {
				break
			}
			original += width
			if character == '\n' || character == '\t' {
				added++
			}
		}
		redactions[index].Position += added
	}
}

func processedVisibleThreads(value retrieval.VisibleThreadsMetadata, runtime *Runtime) (output.VisibleThreads, []output.Redaction) {
	status := "not_requested"
	switch value.Status {
	case retrieval.VisibleThreadsComplete:
		status = "complete"
	case retrieval.VisibleThreadsPartial:
		status = "partial"
	}
	failed := make([]string, len(value.FailedRootIDs))
	redactions := make([]output.Redaction, 0)
	options := presentation.Options{Credentials: []string{runtime.Config.Token}, DisableHeuristics: !runtime.Config.Redact}
	for index, id := range value.FailedRootIDs {
		result := presentation.PreprocessWithOptions(id, options)
		remapLabelPositions(result.Text, result.Redactions)
		failed[index] = presentation.SanitizeLabel(result.Text)
		for _, redaction := range result.Redactions {
			redaction.Field = "retrieval.failedRootId"
			redactions = append(redactions, redaction)
		}
	}
	return output.VisibleThreads{Status: status, HydratedRootCount: value.HydratedRootCount, FailedRootIDs: failed}, redactions
}

func machineCompleteness(value retrieval.Completeness) output.MachineCompleteness {
	switch value {
	case retrieval.CompletenessComplete:
		return output.MachineComplete
	case retrieval.CompletenessTruncated:
		return output.MachineTruncated
	default:
		return output.MachineUnknown
	}
}

func truncatedPointer(value retrieval.Completeness) *bool {
	if value == retrieval.CompletenessUnknown {
		return nil
	}
	result := value == retrieval.CompletenessTruncated
	return &result
}

func (s *rootState) renderRead(outputs []output.MessageOutput, document output.MachineDocument, display readDisplayOptions) error {
	if display.json {
		if _, err := output.WriteMachineJSON(s.streams.out, document); err != nil {
			return outputError{err: err}
		}
		return nil
	}
	if len(outputs) == 0 {
		return writeAll(s.streams.out, []byte("No messages found.\n"))
	}
	dates := output.NewDateFormatter(time.Now, time.Local)
	var formatted string
	if display.color && s.deps.stdoutTTY() {
		formatted = output.FormatPretty(outputs, dates, output.PrettyOptions{Color: true, Relative: display.relative})
	} else if s.deps.stdoutTTY() {
		formatted = output.FormatPretty(outputs, dates, output.PrettyOptions{Relative: display.relative})
	} else {
		formatted = output.FormatMarkdown(outputs, dates, output.MarkdownOptions{Relative: display.relative})
	}
	return writeAll(s.streams.out, []byte(formatted+"\n"))
}

func emitRedactionWarning(s *rootState, runtime *Runtime, machine bool) error {
	if runtime.Config.Redact {
		return nil
	}
	warning := "warning: secret redaction is disabled; output may contain secrets\n"
	if machine {
		s.queueTypedMachineWarning("redaction_disabled", strings.TrimSuffix(warning, "\n"))
		return nil
	}
	return writeAll(s.streams.err, []byte(warning))
}

func readError(message string) error { return readFailure(fmt.Errorf("%s", message)) }
