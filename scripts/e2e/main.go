// Command e2e runs the disposable Mattermost acceptance suite and proves that
// all project-scoped Docker resources are removed afterward.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type runner struct {
	root        string
	composeFile string
	project     string
	port        string
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := runMain(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "Docker E2E failed:", err)
		os.Exit(1)
	}
}

func runMain(ctx context.Context) (runErr error) {
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	composeFile := filepath.Join(root, "tests", "e2e", "compose.yml")
	if _, err = os.Stat(composeFile); err != nil {
		return errors.New("run the Docker E2E command from the repository root")
	}
	port, err := requestedPort(os.Getenv("MM_E2E_PORT"))
	if err != nil {
		return err
	}
	projectSuffix, err := randomHex(4)
	if err != nil {
		return err
	}
	markerSuffix, err := randomHex(8)
	if err != nil {
		return err
	}
	r := runner{
		root:        root,
		composeFile: composeFile,
		project:     fmt.Sprintf("mattermost-cli-e2e-%d-%s", os.Getpid(), projectSuffix),
		port:        port,
	}

	cleanupRequired := true
	defer func() {
		if !cleanupRequired {
			return
		}
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if cleanupErr := r.cleanup(cleanupCtx); cleanupErr != nil {
			if runErr == nil {
				runErr = cleanupErr
			} else {
				runErr = fmt.Errorf("%w; cleanup also failed: %v", runErr, cleanupErr)
			}
		}
	}()

	if _, err = r.docker(ctx, false, false, "up", "-d", "--wait", "--wait-timeout", "180"); err != nil {
		return err
	}
	published, err := r.docker(ctx, true, false, "port", "mattermost", "8065")
	if err != nil {
		return err
	}
	livePort, err := parseLoopbackPublishedPort(published)
	if err != nil {
		return err
	}
	url := "http://127.0.0.1:" + livePort

	if err = r.seed(ctx, "mm-e2e-"+markerSuffix); err != nil {
		return err
	}
	generated, err := r.mmctl(ctx, true, true, "--json", "token", "generate", "sender", "mattermost-cli-e2e")
	if err != nil {
		return err
	}
	var tokens []struct {
		Token string `json:"token"`
	}
	if json.Unmarshal([]byte(generated), &tokens) != nil || len(tokens) != 1 || tokens[0].Token == "" {
		return errors.New("Mattermost did not return one E2E access token")
	}

	binary, err := os.CreateTemp("", r.project+"-mm-")
	if err != nil {
		return err
	}
	binaryPath := binary.Name()
	if err = binary.Close(); err != nil {
		return err
	}
	if err = os.Remove(binaryPath); err != nil {
		return err
	}
	defer os.Remove(binaryPath)
	if _, err = r.command(ctx, false, false, nil, "go", "build", "-tags=e2e", "-o", binaryPath, "./cmd/mm"); err != nil {
		return err
	}
	env := []string{
		"MM_E2E_URL=" + url,
		"MM_E2E_TOKEN=" + tokens[0].Token,
		"MM_E2E_BINARY=" + binaryPath,
		"MM_E2E_MARKER_TEAM=mm-e2e-" + markerSuffix,
	}
	if _, err = r.command(ctx, false, true, env, "go", "test", "-tags=e2e", "-count=1", "./tests/e2e"); err != nil {
		return err
	}
	return nil
}

func (r runner) seed(ctx context.Context, markerTeam string) error {
	if _, err := r.mmctl(ctx, false, false, "--quiet", "user", "create", "--email", "sender@example.test", "--username", "sender", "--password", "E2ePassword1!", "--system-admin", "--email-verified", "--disable-welcome-email"); err != nil {
		return err
	}
	for _, username := range []string{"alice", "bob", "carol", "dave"} {
		if _, err := r.mmctl(ctx, false, false, "--quiet", "user", "create", "--email", username+"@example.test", "--username", username, "--password", "E2ePassword1!", "--email-verified", "--disable-welcome-email"); err != nil {
			return err
		}
	}
	if _, err := r.mmctl(ctx, false, false, "--quiet", "team", "create", "--name", "e2e", "--display-name", "E2E"); err != nil {
		return err
	}
	if _, err := r.mmctl(ctx, false, false, "--quiet", "team", "users", "add", "e2e", "sender", "alice", "bob", "carol", "dave"); err != nil {
		return err
	}
	if _, err := r.mmctl(ctx, false, false, "--quiet", "team", "create", "--name", markerTeam, "--display-name", "Mattermost CLI E2E "+markerTeam); err != nil {
		return err
	}
	_, err := r.mmctl(ctx, false, false, "--quiet", "team", "users", "add", markerTeam, "sender")
	return err
}

func (r runner) cleanup(ctx context.Context) error {
	if _, err := r.docker(ctx, false, false, "down", "--volumes", "--remove-orphans"); err != nil {
		return err
	}
	containers, err := r.docker(ctx, true, false, "ps", "-aq")
	if err != nil {
		return err
	}
	volumes, err := r.command(ctx, true, false, nil, "docker", "volume", "ls", "-q", "--filter", "label=com.docker.compose.project="+r.project)
	if err != nil {
		return err
	}
	networks, err := r.command(ctx, true, false, nil, "docker", "network", "ls", "-q", "--filter", "label=com.docker.compose.project="+r.project)
	if err != nil {
		return err
	}
	if strings.TrimSpace(containers) != "" || strings.TrimSpace(volumes) != "" || strings.TrimSpace(networks) != "" {
		return errors.New("Docker E2E cleanup left project resources behind")
	}
	return nil
}

func (r runner) mmctl(ctx context.Context, capture, sensitive bool, args ...string) (string, error) {
	return r.docker(ctx, capture, sensitive, append([]string{"exec", "-T", "mattermost", "mmctl", "--local"}, args...)...)
}

func (r runner) docker(ctx context.Context, capture, sensitive bool, args ...string) (string, error) {
	prefix := []string{"compose", "-p", r.project, "-f", r.composeFile}
	return r.command(ctx, capture, sensitive, []string{"MM_E2E_PORT=" + r.port}, "docker", append(prefix, args...)...)
}

func (r runner) command(ctx context.Context, capture, sensitive bool, env []string, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = r.root
	cmd.Env = append(os.Environ(), env...)
	if !capture {
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return "", fmt.Errorf("%s failed: %w", name, err)
		}
		return "", nil
	}
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if !sensitive {
			_, _ = os.Stderr.WriteString(stdout.String())
			_, _ = os.Stderr.WriteString(stderr.String())
		}
		return "", fmt.Errorf("%s failed: %w", name, err)
	}
	return stdout.String(), nil
}

func requestedPort(value string) (string, error) {
	if value == "" {
		return "0", nil
	}
	port, err := strconv.Atoi(value)
	if err != nil || strconv.Itoa(port) != value || port < 0 || port > 65535 {
		return "", errors.New("MM_E2E_PORT must be a canonical integer from 0 through 65535")
	}
	return value, nil
}

func parseLoopbackPublishedPort(value string) (string, error) {
	line := strings.TrimSpace(value)
	if strings.ContainsAny(line, "\r\n") {
		return "", errors.New("Docker reported multiple Mattermost E2E bindings")
	}
	host, port, err := net.SplitHostPort(line)
	if err != nil {
		return "", errors.New("Docker did not report a valid Mattermost E2E binding")
	}
	ip := net.ParseIP(host)
	number, parseErr := strconv.Atoi(port)
	if ip == nil || !ip.IsLoopback() || parseErr != nil || number < 1 || number > 65535 {
		return "", errors.New("Docker Mattermost E2E binding is not loopback-only")
	}
	return strconv.Itoa(number), nil
}

func randomHex(bytes int) (string, error) {
	value := make([]byte, bytes)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}
