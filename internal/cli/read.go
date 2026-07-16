package cli

import (
	"context"
	"fmt"
	"time"
	"unicode/utf16"

	"github.com/ardasevinc/mattermost-cli/internal/mattermost"
	"github.com/ardasevinc/mattermost-cli/internal/normalization"
	"github.com/ardasevinc/mattermost-cli/internal/output"
	"github.com/ardasevinc/mattermost-cli/internal/presentation"
	"github.com/ardasevinc/mattermost-cli/internal/retrieval"
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
				return nil, nil, err
			}
			for _, user := range items {
				users[user.ID] = user
			}
		}
	}
	return normalization.NormalizePosts(posts, users, myUserID, runtime.Config.URL, presentation.Options{
		Credentials: []string{runtime.Config.Token}, DisableHeuristics: !runtime.Config.Redact,
	})
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
	return output.Channel{
		ID: clean(raw.ID, "channel.id"), Type: typeName, Name: clean(raw.Name, "channel.name"),
		DisplayName: clean(raw.DisplayName, "channel.displayName"), MetadataStatus: "resolved",
	}, redactions
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

func warnRedactionDisabled(s *rootState, runtime *Runtime) error {
	if runtime.Config.Redact {
		return nil
	}
	return writeAll(s.streams.err, []byte("warning: secret redaction is disabled; output may contain secrets\n"))
}

func readError(message string) error { return readFailure(fmt.Errorf("%s", message)) }
