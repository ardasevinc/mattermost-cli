package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/ardasevinc/mattermost-cli/internal/mattermost"
	"github.com/ardasevinc/mattermost-cli/internal/output"
	"github.com/ardasevinc/mattermost-cli/internal/presentation"
)

type watchFlags struct{ team, dm string }

type watchOutputFailure struct{}

func (watchOutputFailure) Error() string { return "watch output failed" }

type watchTerminalFailure struct{}

func (watchTerminalFailure) Error() string { return "watch terminated" }

var ErrSignalCancellation = errors.New("command canceled by signal")

func newWatchCommand(state *rootState) *cobra.Command {
	flags := new(watchFlags)
	command := &cobra.Command{Use: "watch [channel]", Short: "Watch posted events in a channel or direct message", Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		channel := ""
		if len(args) == 1 {
			channel = args[0]
		}
		err := runWatch(cmd, state, channel, *flags)
		if err != nil && errors.Is(context.Cause(cmd.Context()), ErrSignalCancellation) {
			if !watchStreamFailed(state, err) {
				state.takeMachineWarnings()
				return nil
			}
		}
		return err
	}}
	command.Flags().StringVar(&flags.team, "team", "", "team name (auto-detected for one team)")
	command.Flags().StringVar(&flags.dm, "dm", "", "direct-message username")
	return command
}

func runWatch(cmd *cobra.Command, state *rootState, channelName string, flags watchFlags) error {
	channelSet := strings.TrimSpace(channelName) != ""
	dmSet := strings.TrimSpace(flags.dm) != ""
	if channelSet == dmSet {
		return invalidFailure("provide exactly one channel name or --dm username")
	}
	if flagChanged(cmd, "dm") && !dmSet {
		return invalidFailure("--dm cannot be empty")
	}
	if flagChanged(cmd, "team") && strings.TrimSpace(flags.team) == "" {
		return invalidFailure("--team cannot be empty")
	}
	if dmSet && flagChanged(cmd, "team") {
		return invalidFailure("--team cannot be combined with --dm")
	}
	display, err := state.readDisplay(cmd)
	if err != nil {
		return err
	}
	runtime, err := state.runtimeFor(cmd)
	if err != nil {
		return err
	}
	me, err := runtime.Users.Current(cmd.Context())
	if err != nil {
		return readFailure(err)
	}
	var channel mattermost.Channel
	knownSenders := map[string]string{me.ID: me.Username}
	targetLabel := ""
	if dmSet {
		username := strings.TrimPrefix(flags.dm, "@")
		partner, lookupErr := runtime.Users.ByUsername(cmd.Context(), username)
		if lookupErr != nil {
			return readFailure(lookupErr)
		}
		if partner.ID == me.ID {
			return invalidFailure("cannot watch a direct message with the current user")
		}
		knownSenders[partner.ID] = partner.Username
		channels, listErr := runtime.Channels.DirectList(cmd.Context(), me.ID)
		if listErr != nil {
			return readFailure(listErr)
		}
		for _, candidate := range channels {
			parts := strings.Split(candidate.Name, "__")
			if len(parts) == 2 && ((parts[0] == me.ID && parts[1] == partner.ID) || (parts[1] == me.ID && parts[0] == partner.ID)) {
				if channel.ID != "" && channel.ID != candidate.ID {
					return readError("Mattermost returned ambiguous direct-message channels")
				}
				channel = candidate
			}
		}
		if channel.ID == "" {
			return readError("direct-message channel was not found")
		}
		targetLabel = "DMs with @" + presentWatchLabel(username, runtime.Config.Token, state.disableHeuristics)
	} else {
		team, teamErr := runtime.Teams.Resolve(cmd.Context(), me.ID, flags.team)
		if teamErr != nil {
			return readFailure(teamErr)
		}
		channel, err = runtime.Channels.ByName(cmd.Context(), team.ID, channelName)
		if err != nil {
			return readFailure(err)
		}
		if _, err = runtime.Channels.Member(cmd.Context(), channel.ID, me.ID); err != nil {
			return readFailure(err)
		}
		targetLabel = "#" + presentWatchLabel(channel.Name, runtime.Config.Token, state.disableHeuristics)
	}
	if err := emitWatchRedactionWarning(state, runtime, display.json); err != nil {
		return err
	}
	var sink mattermost.WatchSink
	if display.json {
		sink = output.JSONLWatchSink{Events: state.streams.out, Diagnostics: state.streams.err, DisableHeuristics: state.disableHeuristics}
	} else {
		sink = &humanWatchSink{out: state.streams.out, err: state.streams.err, token: runtime.Config.Token, disableHeuristics: state.disableHeuristics, color: display.color && runtime.StdoutTTY}
		if err := writeWatchLine(state.streams.err, "Watching "+targetLabel+" (Ctrl+C to stop)\n"); err != nil {
			return watchOutputFailure{}
		}
	}
	sink = &watchSenderBindingSink{next: sink, known: knownSenders}
	if display.json {
		for _, warning := range state.takeMachineWarnings() {
			if err := sink.Diagnostic(mattermost.WatchDiagnostic{Type: "warning", Code: warning.code, Recovery: "none", Timestamp: time.Now().UTC(), Message: warning.message}); err != nil {
				return watchOutputFailure{}
			}
		}
	}
	if cmd.Context().Err() != nil && errors.Is(context.Cause(cmd.Context()), ErrSignalCancellation) {
		return nil
	}
	err = state.deps.watch(cmd.Context(), mattermost.WatchOptions{URL: runtime.Config.URL, Token: runtime.Config.Token, ChannelID: channel.ID, Sink: sink})
	if err == nil {
		return nil
	}
	if errors.Is(err, mattermost.ErrWatchSink) {
		return watchOutputFailure{}
	}
	if errors.Is(err, context.Canceled) && errors.Is(context.Cause(cmd.Context()), ErrSignalCancellation) {
		return nil
	}
	code, recovery, message := "watch_failed", "none", "WebSocket watch failed."
	switch {
	case errors.Is(err, mattermost.ErrWatchAuthentication):
		code, recovery, message = "authentication", "check_token", "WebSocket authentication failed; check the configured token."
	case errors.Is(err, mattermost.ErrWatchRetries):
		code, recovery, message = "reconnect_exhausted", "retry_later", "WebSocket reconnect limit reached; retry later."
	case errors.Is(err, mattermost.ErrInvalidWatchOptions):
		code, message = "invalid_options", "WebSocket watch configuration is invalid."
	case errors.Is(err, context.Canceled):
		code, message = "canceled", "WebSocket watch was canceled by the caller."
	}
	if sinkErr := sink.Diagnostic(mattermost.WatchDiagnostic{Type: "terminal", Code: code, Recovery: recovery, Timestamp: time.Now().UTC(), Message: message, Fatal: true}); sinkErr != nil {
		return watchOutputFailure{}
	}
	return watchTerminalFailure{}
}

type humanWatchSink struct {
	out, err          io.Writer
	token             string
	disableHeuristics bool
	color             bool
}

func (s *humanWatchSink) Post(post mattermost.WatchPost, _ mattermost.Sequence) error {
	sender := presentWatchLabel(post.SenderName, s.token, s.disableHeuristics)
	message := presentWatchMessage(post.Message, s.token, s.disableHeuristics)
	return writeWatchLine(s.out, output.FormatWatchHumanLine(time.UnixMilli(post.CreateAt), sender, message, s.color)+"\n")
}

type watchSenderBindingSink struct {
	next  mattermost.WatchSink
	known map[string]string
}

func (s *watchSenderBindingSink) Post(post mattermost.WatchPost, sequence mattermost.Sequence) error {
	if canonical, ok := s.known[post.UserID]; ok {
		post.SenderName = canonical
	} else if post.SenderName == "" {
		post.SenderName = "unknown"
	}
	return s.next.Post(post, sequence)
}

func (s *watchSenderBindingSink) Diagnostic(value mattermost.WatchDiagnostic) error {
	return s.next.Diagnostic(value)
}

func emitWatchRedactionWarning(state *rootState, runtime *Runtime, machine bool) error {
	if runtime.Config.Redact {
		return nil
	}
	const warning = "Warning: Secret redaction is disabled. Output may contain secrets."
	if machine {
		state.queueTypedMachineWarning("redaction_disabled", warning)
		return nil
	}
	return writeAll(state.streams.err, []byte(warning+"\n"))
}

func watchStreamFailed(state *rootState, err error) bool {
	var outputFailure outputError
	var watchFailure watchOutputFailure
	if errors.As(err, &outputFailure) || errors.As(err, &watchFailure) {
		return true
	}
	tracker, ok := state.streams.out.(interface{ Failed() bool })
	return ok && tracker.Failed()
}
func (s *humanWatchSink) Diagnostic(value mattermost.WatchDiagnostic) error {
	message := "WebSocket diagnostic."
	switch value.Type {
	case "reconnect":
		message = fmt.Sprintf("WebSocket disconnected; reconnecting in %dms (attempt %d); no REST backfill.", value.Delay.Milliseconds(), *value.Attempt)
	case "sequence_gap":
		message = fmt.Sprintf("Warning: WebSocket sequence gap detected (expected %d, received %d); live events may be missing; no REST backfill.", *value.Expected, *value.Received)
	case "connection_changed":
		message = "Warning: WebSocket connection changed; live events may be missing; no REST backfill."
	case "malformed":
		message = "Warning: Malformed WebSocket event skipped."
	case "disconnected":
		message = "WebSocket disconnected; live events may be missing."
	case "warning":
		message = value.Message
	case "terminal":
		message = value.Message
	}
	return writeWatchLine(s.err, message+"\n")
}

func presentWatchLabel(value, token string, disable bool) string {
	value = presentation.SanitizeLabel(value)
	return presentation.PreprocessWithOptions(value, presentation.Options{Credentials: []string{token}, DisableHeuristics: disable}).Text
}
func presentWatchMessage(value, token string, disable bool) string {
	return presentation.PreprocessWithOptions(value, presentation.Options{Credentials: []string{token}, DisableHeuristics: disable}).Text
}
func writeWatchLine(writer io.Writer, value string) error {
	written, err := writer.Write([]byte(value))
	if err != nil || written != len(value) {
		return io.ErrShortWrite
	}
	return nil
}
