package cli

import (
	"bytes"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/ardasevinc/mattermost-cli/internal/mattermost"
	"github.com/ardasevinc/mattermost-cli/internal/output"
	"github.com/ardasevinc/mattermost-cli/internal/presentation"
)

func newWhoAmICommand(state *rootState) *cobra.Command {
	return &cobra.Command{Use: "whoami", Short: "Show the authenticated identity", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error { return runWhoAmI(cmd, state) }}
}
func newTeamsCommand(state *rootState) *cobra.Command {
	return &cobra.Command{Use: "teams", Short: "List team memberships", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error { return runTeams(cmd, state) }}
}

type usersFlags struct{ limit, team string }

func newUsersCommand(state *rootState) *cobra.Command {
	flags := new(usersFlags)
	command := &cobra.Command{Use: "users [query]", Short: "List users", Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error { return runUsers(cmd, state, *flags, args) }}
	command.Flags().StringVarP(&flags.limit, "limit", "l", "20", "maximum users")
	command.Flags().StringVar(&flags.team, "team", "", "exact team name or display name")
	return command
}

type channelsFlags struct{ kind string }

func newChannelsCommand(state *rootState) *cobra.Command {
	flags := new(channelsFlags)
	command := &cobra.Command{Use: "channels", Short: "List account channels", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error { return runChannels(cmd, state, *flags) }}
	command.Flags().StringVar(&flags.kind, "type", "all", "channel type: all, dm, public, private, or group")
	return command
}

func identityOptions(runtime *Runtime) presentation.Options {
	return presentation.Options{Credentials: []string{runtime.Config.Token}, DisableHeuristics: !runtime.Config.Redact}
}
func rawIdentity(user mattermost.User) output.RawIdentity {
	display := strings.TrimSpace(strings.Join([]string{user.FirstName, user.LastName}, " "))
	return output.RawIdentity{ID: user.ID, Username: user.Username, DisplayName: display, Nickname: user.Nickname, Roles: strings.Fields(user.Roles)}
}
func rawTeams(values []mattermost.Team) []output.RawTeam {
	result := make([]output.RawTeam, len(values))
	for i, value := range values {
		result[i] = output.RawTeam{ID: value.ID, Name: value.Name, DisplayName: value.DisplayName, Type: value.Type}
	}
	return result
}
func rawUsers(values []mattermost.User) []output.RawUser {
	result := make([]output.RawUser, len(values))
	for i, value := range values {
		display := strings.TrimSpace(strings.Join([]string{value.FirstName, value.LastName}, " "))
		result[i] = output.RawUser{ID: value.ID, Username: value.Username, DisplayName: display, Nickname: value.Nickname}
	}
	return result
}

func runWhoAmI(cmd *cobra.Command, state *rootState) error {
	runtime, err := state.runtimeFor(cmd)
	if err != nil {
		return err
	}
	if err = emitRedactionWarning(state, runtime, state.flags.json); err != nil {
		return err
	}
	user, err := runtime.Users.Current(cmd.Context())
	if err != nil {
		return readFailure(err)
	}
	document, err := output.NewWhoAmIEnvelope(rawIdentity(user), identityOptions(runtime))
	if err != nil {
		return readFailure(err)
	}
	if state.flags.json {
		return writeIdentityMachine(state, document)
	}
	labels := []string{}
	if document.Data.DisplayName != nil {
		labels = append(labels, *document.Data.DisplayName)
	}
	if document.Data.Nickname != nil {
		labels = append(labels, "aka "+*document.Data.Nickname)
	}
	suffix := ""
	if len(labels) > 0 {
		suffix = " (" + strings.Join(labels, ", ") + ")"
	}
	roles := "none"
	if len(document.Data.Roles) > 0 {
		roles = strings.Join(document.Data.Roles, ", ")
	}
	return writeAll(state.streams.out, []byte(fmt.Sprintf("@%s%s [%s]\nRoles: %s\n", document.Data.Username, suffix, document.Data.ID, roles)))
}

func runTeams(cmd *cobra.Command, state *rootState) error {
	runtime, err := state.runtimeFor(cmd)
	if err != nil {
		return err
	}
	if err = emitRedactionWarning(state, runtime, state.flags.json); err != nil {
		return err
	}
	me, err := runtime.Users.Current(cmd.Context())
	if err != nil {
		return readFailure(err)
	}
	membership, err := runtime.Teams.List(cmd.Context(), me.ID)
	if err != nil {
		return readFailure(err)
	}
	document, err := output.NewTeamsEnvelope(rawTeams(membership.Items()), identityOptions(runtime))
	if err != nil {
		return readFailure(err)
	}
	if state.flags.json {
		return writeIdentityMachine(state, document)
	}
	if len(document.Teams) == 0 {
		return writeAll(state.streams.out, []byte("No teams found.\n"))
	}
	var text strings.Builder
	for _, team := range document.Teams {
		display := ""
		if team.DisplayName != nil && *team.DisplayName != team.Name {
			display = " (" + *team.DisplayName + ")"
		}
		fmt.Fprintf(&text, "%s%s [%s] %s\n", team.Name, display, team.ID, team.Type)
	}
	return writeAll(state.streams.out, []byte(text.String()))
}

func runUsers(cmd *cobra.Command, state *rootState, flags usersFlags, args []string) error {
	limit, err := positiveInteger(flags.limit)
	if err != nil {
		return err
	}
	if limit > 1000 {
		return invalidFailure("--limit exceeds the users endpoint ceiling of 1000")
	}
	if flagChanged(cmd, "team") && strings.TrimSpace(flags.team) == "" {
		return invalidFailure("--team cannot be empty")
	}
	query := ""
	if len(args) > 0 {
		query = strings.TrimSpace(args[0])
	}
	runtime, err := state.runtimeFor(cmd)
	if err != nil {
		return err
	}
	if err = emitRedactionWarning(state, runtime, state.flags.json); err != nil {
		return err
	}
	teamID := ""
	if flags.team != "" {
		me, currentErr := runtime.Users.Current(cmd.Context())
		if currentErr != nil {
			return readFailure(currentErr)
		}
		team, resolutionErr := runtime.Teams.Resolve(cmd.Context(), me.ID, flags.team)
		if resolutionErr != nil {
			return readFailure(resolutionErr)
		}
		teamID = team.ID
	}
	result, err := runtime.Users.Directory(cmd.Context(), query, teamID, limit)
	if err != nil {
		return readFailure(err)
	}
	probe := int64(len(result.Users))
	if result.Truncated == nil {
		if query == "" {
			probe = 200
		} else {
			probe = 1000
		}
	} else if *result.Truncated {
		probe++
	}
	document, err := output.NewUsersEnvelope(rawUsers(result.Users), output.UsersRetrievalProof{RequestedLimit: int64(limit), ProbeCount: probe, Query: query, TeamID: teamID}, identityOptions(runtime))
	if err != nil {
		return readFailure(err)
	}
	if state.flags.json {
		return writeIdentityMachine(state, document)
	}
	if len(document.Users) == 0 {
		return writeAll(state.streams.out, []byte("No users found.\n"))
	}
	var text strings.Builder
	for _, user := range document.Users {
		labels := []string{}
		if user.DisplayName != nil {
			labels = append(labels, *user.DisplayName)
		}
		if user.Nickname != nil {
			labels = append(labels, "aka "+*user.Nickname)
		}
		suffix := ""
		if len(labels) > 0 {
			suffix = " (" + strings.Join(labels, ", ") + ")"
		}
		fmt.Fprintf(&text, "@%s%s [%s]\n", user.Username, suffix, user.ID)
	}
	coverage := "unknown"
	if document.Retrieval.Truncated != nil {
		coverage = map[bool]string{true: "truncated", false: "complete"}[*document.Retrieval.Truncated]
	}
	fmt.Fprintf(&text, "Showing %d of up to %d users (coverage: %s).\n", len(document.Users), limit, coverage)
	return writeAll(state.streams.out, []byte(text.String()))
}

func runChannels(cmd *cobra.Command, state *rootState, flags channelsFlags) error {
	valid := map[string]string{"all": "", "dm": "D", "public": "O", "private": "P", "group": "G"}
	typeCode, ok := valid[flags.kind]
	if !ok {
		return invalidFailure("--type must be one of: all, dm, public, private, group")
	}
	display, err := state.readDisplay(cmd)
	if err != nil {
		return err
	}
	runtime, err := state.runtimeFor(cmd)
	if err != nil {
		return err
	}
	if err = emitRedactionWarning(state, runtime, display.json); err != nil {
		return err
	}
	me, err := runtime.Users.Current(cmd.Context())
	if err != nil {
		return readFailure(err)
	}
	selectedTypes := []string{"O", "P", "D", "G"}
	if typeCode != "" {
		selectedTypes = []string{typeCode}
	}
	selection, err := runtime.Channels.ListSelected(cmd.Context(), me.ID, selectedTypes...)
	if err != nil {
		return readFailure(err)
	}
	channels := selection.Channels
	teams := map[string]mattermost.Team{}
	for _, team := range selection.Membership.Items() {
		teams[team.ID] = team
	}
	peers := map[string]mattermost.User{}
	peerIDs := []string{}
	seenPeers := map[string]bool{}
	for _, channel := range channels {
		if channel.Type != "D" {
			continue
		}
		parts := strings.Split(channel.Name, "__")
		other := parts[0]
		if other == me.ID {
			other = parts[1]
		}
		if !seenPeers[other] {
			seenPeers[other] = true
			peerIDs = append(peerIDs, other)
		}
	}
	sort.Strings(peerIDs)
	for start := 0; start < len(peerIDs); start += 200 {
		end := start + 200
		if end > len(peerIDs) {
			end = len(peerIDs)
		}
		users, userErr := runtime.Users.ByIDs(cmd.Context(), peerIDs[start:end])
		if userErr != nil {
			return readFailure(userErr)
		}
		for _, user := range users {
			peers[user.ID] = user
		}
	}
	raw := make([]output.RawChannel, len(channels))
	for i, channel := range channels {
		value := output.RawChannel{ID: channel.ID, Type: channel.Type, Name: channel.Name, DisplayName: channel.DisplayName, TeamID: channel.TeamID, LastPostAt: channel.LastPostAt, TotalMsgCount: channel.TotalMsgCount}
		if channel.Type == "D" {
			parts := strings.Split(channel.Name, "__")
			other := parts[0]
			if other == me.ID {
				other = parts[1]
			}
			value.DirectUsername = peers[other].Username
		} else if channel.Type == "O" || channel.Type == "P" {
			team, exists := teams[channel.TeamID]
			if !exists {
				return readError("Mattermost returned incomplete team metadata for a channel")
			}
			rawTeam := output.RawTeam{ID: team.ID, Name: team.Name, DisplayName: team.DisplayName, Type: team.Type}
			value.Team = &rawTeam
		}
		raw[i] = value
	}
	document, err := output.NewChannelsEnvelope(raw, identityOptions(runtime))
	if err != nil {
		return readFailure(err)
	}
	if display.json {
		return writeIdentityMachine(state, document)
	}
	return renderHumanChannels(state, document, display.relative)
}

func writeIdentityMachine(state *rootState, document output.MachineDocument) error {
	var wire bytes.Buffer
	if _, err := output.WriteMachineJSON(&wire, document); err != nil {
		return readFailure(err)
	}
	return writeAll(state.streams.out, wire.Bytes())
}
func renderHumanChannels(state *rootState, document output.ChannelsEnvelope, relative bool) error {
	if len(document.Channels) == 0 {
		return writeAll(state.streams.out, []byte("\nTotal: 0 channels\n"))
	}
	labels := map[string]string{"public": "Public Channels", "private": "Private Channels", "group": "Group Messages", "dm": "Direct Messages"}
	order := []string{"public", "private", "group", "dm"}
	formatter := output.NewDateFormatter(time.Now, time.Local)
	var text strings.Builder
	for _, kind := range order {
		items := []output.ChannelItem{}
		for _, item := range document.Channels {
			if item.Type == kind {
				items = append(items, item)
			}
		}
		if len(items) == 0 {
			continue
		}
		fmt.Fprintf(&text, "\n%s:\n\n", labels[kind])
		for _, item := range items {
			last := "never"
			if item.LastPost != nil {
				if relative {
					last = formatter.FormatRelativeTime(item.LastPost.Time)
				} else {
					last = formatter.FormatDate(item.LastPost.Time, true)
				}
			}
			label := item.Name
			if item.Team != nil {
				label = item.Team.Name + "/#" + item.Name
			}
			display := ""
			if item.DisplayName != nil {
				display = " (" + *item.DisplayName + ")"
			}
			fmt.Fprintf(&text, "  %-25s%-25s [%s] %d msgs, last: %s\n", label, display, item.ID, item.MessageCount, last)
		}
	}
	fmt.Fprintf(&text, "\nTotal: %d channels\n", len(document.Channels))
	return writeAll(state.streams.out, []byte(text.String()))
}
