package cli

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"

	"github.com/spf13/cobra"

	"github.com/ardasevinc/mattermost-cli/v2/internal/api"
	"github.com/ardasevinc/mattermost-cli/v2/internal/config"
	"github.com/ardasevinc/mattermost-cli/v2/internal/mattermost"
	"github.com/ardasevinc/mattermost-cli/v2/internal/presentation"
	"github.com/ardasevinc/mattermost-cli/v2/internal/serverurl"
)

type clientFactory func(baseURL, token string) (*api.Client, error)

type dependencies struct {
	lookupEnv config.LookupEnv
	homeDir   func() (string, error)
	newClient clientFactory
	stdoutTTY func() bool
	watch     func(context.Context, mattermost.WatchOptions) error
}

func defaultDependencies(out any) dependencies {
	return dependencies{
		lookupEnv: os.LookupEnv,
		homeDir:   os.UserHomeDir,
		newClient: func(baseURL, token string) (*api.Client, error) { return api.New(baseURL, token) },
		stdoutTTY: func() bool {
			file, ok := out.(*os.File)
			if !ok {
				return false
			}
			info, err := file.Stat()
			return err == nil && info.Mode()&os.ModeCharDevice != 0
		},
		watch: mattermost.Watch,
	}
}

type Runtime struct {
	Config    config.Resolved
	Client    *api.Client
	Users     *mattermost.Users
	Teams     *mattermost.Teams
	Channels  *mattermost.Channels
	Posts     *mattermost.Posts
	StdoutTTY bool
}

func (r *Runtime) Close() {
	if r != nil && r.Client != nil {
		r.Client.Close()
	}
}

type rootState struct {
	streams streams
	deps    dependencies
	flags   runtimeFlags

	mu                sync.Mutex
	runtime           *Runtime
	runtimeErr        error
	resolved          bool
	warned            bool
	releases          []func()
	credentials       []string
	pendingWarnings   []machineWarning
	semanticExit      int
	disableHeuristics bool
	stageTTLSeconds   int64
	stagePruneSeconds int64
}

type runtimeFlags struct {
	url        string
	token      string
	redact     bool
	noRedact   bool
	json       bool
	noColor    bool
	relative   bool
	noRelative bool
	threads    bool
	noThreads  bool
}

func (s *rootState) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.runtime != nil {
		s.runtime.Close()
		s.runtime = nil
	}
}

func (s *rootState) releaseCredentials() {
	s.mu.Lock()
	releases := s.releases
	s.releases = nil
	s.mu.Unlock()
	for i := len(releases) - 1; i >= 0; i-- {
		releases[i]()
	}
}

func (s *rootState) runtimeFor(cmd *cobra.Command) (*Runtime, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.resolved {
		return s.runtime, s.runtimeErr
	}
	s.resolved = true

	home, err := s.deps.homeDir()
	if err != nil {
		s.runtimeErr = configFailure("could not resolve the home directory")
		return nil, s.runtimeErr
	}
	paths, err := config.ResolvePaths(home, s.deps.lookupEnv)
	if err != nil {
		s.runtimeErr = configFailure(err.Error())
		return nil, s.runtimeErr
	}
	file := config.Load(paths)
	if file.Config.Token != "" {
		s.releases = append(s.releases, presentation.ActiveCredentials.Register(file.Config.Token))
		s.credentials = append(s.credentials, file.Config.Token)
	}
	if file.Error == config.FileErrorRead || file.Unsafe != "" {
		s.runtimeErr = configFailure("could not safely read the Mattermost configuration")
		return nil, s.runtimeErr
	}
	if file.Error == config.FileErrorParse {
		s.runtimeErr = configFailure("could not parse the Mattermost configuration")
		return nil, s.runtimeErr
	}
	if file.WritableByOthers {
		s.runtimeErr = configFailure("Mattermost configuration must not be writable by other users")
		return nil, s.runtimeErr
	}
	if file.InsecurePermissions && file.Config.Token != "" {
		s.runtimeErr = configFailure("Mattermost configuration containing a token must not be accessible by other users")
		return nil, s.runtimeErr
	}
	if warning := file.Warning(); warning != "" && !s.warned {
		warning = presentation.SanitizeLabel(presentation.Preprocess(warning, s.credentials).Text)
		if s.flags.json {
			s.pendingWarnings = append(s.pendingWarnings, machineWarning{code: "configuration_warning", message: "warning: " + warning})
		} else {
			if err := writeAll(s.streams.err, []byte("warning: "+warning+"\n")); err != nil {
				s.runtimeErr = err
				return nil, err
			}
		}
		s.warned = true
	}

	redact, err := s.redactOption(cmd)
	if err != nil {
		s.runtimeErr = err
		return nil, err
	}
	resolved := config.Resolve(config.Options{URL: s.flags.url, Token: s.flags.token, Redact: redact}, s.deps.lookupEnv, file)
	if resolved.URL == "" || resolved.Token == "" {
		s.runtimeErr = configFailure("Mattermost URL and token are required")
		return nil, s.runtimeErr
	}
	normalized, err := serverurl.Normalize(resolved.URL)
	if err != nil {
		s.runtimeErr = configFailure(err.Error())
		return nil, s.runtimeErr
	}
	resolved.URL = normalized
	client, err := s.deps.newClient(resolved.URL, resolved.Token)
	if err != nil {
		s.runtimeErr = configFailure("could not initialize the Mattermost client")
		return nil, s.runtimeErr
	}
	s.runtime = &Runtime{
		Config: resolved, Client: client, StdoutTTY: s.deps.stdoutTTY(),
		Users: mattermost.NewUsers(client), Teams: mattermost.NewTeams(client),
		Channels: mattermost.NewChannels(client), Posts: mattermost.NewPosts(client),
	}
	return s.runtime, nil
}

func (s *rootState) redactOption(cmd *cobra.Command) (*bool, error) {
	redactFlag := cmd.Flags().Lookup("redact")
	noRedactFlag := cmd.Flags().Lookup("no-redact")
	redactChanged := redactFlag != nil && redactFlag.Changed
	noRedactChanged := noRedactFlag != nil && noRedactFlag.Changed
	if redactChanged && noRedactChanged {
		return nil, invalidFailure("--redact and --no-redact cannot be used together")
	}
	var redact *bool
	if redactChanged {
		value := s.flags.redact
		redact = &value
	} else if noRedactChanged {
		value := !s.flags.noRedact
		redact = &value
	}
	s.disableHeuristics = redact != nil && !*redact
	return redact, nil
}

func (s *rootState) flushMachineWarnings() error {
	s.mu.Lock()
	items := s.pendingWarnings
	s.pendingWarnings = nil
	s.mu.Unlock()
	if len(items) == 0 {
		return nil
	}
	var warnings strings.Builder
	for _, item := range items {
		warnings.WriteString(item.message)
		warnings.WriteByte('\n')
	}
	return writeAll(s.streams.err, []byte(warnings.String()))
}

func (s *rootState) queueMachineWarning(message string) {
	s.mu.Lock()
	s.pendingWarnings = append(s.pendingWarnings, machineWarning{code: "configuration_warning", message: strings.TrimSuffix(message, "\n")})
	s.mu.Unlock()
}

type machineWarning struct{ code, message string }

func (s *rootState) queueTypedMachineWarning(code, message string) {
	s.mu.Lock()
	s.pendingWarnings = append(s.pendingWarnings, machineWarning{code, message})
	s.mu.Unlock()
}
func (s *rootState) takeMachineWarnings() []machineWarning {
	s.mu.Lock()
	defer s.mu.Unlock()
	items := append([]machineWarning(nil), s.pendingWarnings...)
	s.pendingWarnings = nil
	return items
}

func (s *rootState) setSemanticExit(code int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if code > s.semanticExit {
		s.semanticExit = code
	}
}

func (s *rootState) semanticExitCode() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.semanticExit
}

type errorClass uint8

const (
	classInvalid errorClass = iota
	classRead
)

type classifiedError struct {
	class errorClass
	code  string
	msg   string
}

func (e classifiedError) Error() string { return e.msg }

func invalidFailure(message string) error {
	return classifiedError{class: classInvalid, code: "invalid_input", msg: message}
}
func configFailure(message string) error {
	return classifiedError{class: classRead, code: "configuration", msg: message}
}

type operationFailure struct {
	class errorClass
	code  string
	err   error
}

func (e operationFailure) Error() string { return e.err.Error() }
func (e operationFailure) Unwrap() error { return e.err }

// readFailure and authFailure preserve the v2 exit contract at command boundaries.
func readFailure(err error) error {
	code := "read_failed"
	var remote *api.APIError
	if errors.As(err, &remote) {
		switch remote.Status {
		case 401:
			code = "authentication"
		case 403:
			code = "authorization"
		}
	}
	return operationFailure{class: classRead, code: code, err: err}
}
func authFailure(err error) error {
	return operationFailure{class: classRead, code: "authentication", err: err}
}
func internalFailure(err error) error {
	return operationFailure{class: classRead, code: "internal", err: err}
}

func exitCode(err error) int {
	var applyFailure applyCommandFailure
	if errors.As(err, &applyFailure) {
		return applyFailure.exit
	}
	var outputFailure outputError
	if errors.As(err, &outputFailure) {
		return 3
	}
	var local localStateFailure
	if errors.As(err, &local) {
		return 6
	}
	var classified classifiedError
	if errors.As(err, &classified) && classified.class == classRead {
		return 3
	}
	var operation operationFailure
	if errors.As(err, &operation) && operation.class == classRead {
		return 3
	}
	return 2
}

func earlyTokens(args []string) []string {
	var tokens []string
	for index := 0; index < len(args); index++ {
		arg := args[index]
		if arg == "--token" && index+1 < len(args) {
			tokens = append(tokens, args[index+1])
			index++
			continue
		}
		if strings.HasPrefix(arg, "--token=") {
			tokens = append(tokens, strings.TrimPrefix(arg, "--token="))
			continue
		}
		if value, next, ok := shortTokenValue(args, index); ok {
			tokens = append(tokens, value)
			if next {
				index++
			}
		}
	}
	return tokens
}

func shortTokenValue(args []string, index int) (value string, consumedNext, ok bool) {
	arg := args[index]
	if !strings.HasPrefix(arg, "-") || strings.HasPrefix(arg, "--") || len(arg) < 2 {
		return "", false, false
	}
	shorthand := strings.TrimPrefix(arg, "-")
	for position := 0; position < len(shorthand); position++ {
		switch shorthand[position] {
		case 'r':
			continue
		case 't':
			value := strings.TrimPrefix(shorthand[position+1:], "=")
			if value != "" {
				return value, false, true
			}
			if index+1 < len(args) {
				return args[index+1], true, true
			}
			return "", false, false
		default:
			return "", false, false
		}
	}
	return "", false, false
}

func bestEffortFileToken(deps dependencies) string {
	home, err := deps.homeDir()
	if err != nil {
		return ""
	}
	paths, err := config.ResolvePaths(home, deps.lookupEnv)
	if err != nil {
		return ""
	}
	return config.Load(paths).Config.Token
}
