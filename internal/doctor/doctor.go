// Package doctor performs the fixed, read-only Mattermost readiness checks.
package doctor

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/ardasevinc/mattermost-cli/v2/internal/api"
	"github.com/ardasevinc/mattermost-cli/v2/internal/config"
	"github.com/ardasevinc/mattermost-cli/v2/internal/presentation"
	"github.com/ardasevinc/mattermost-cli/v2/internal/serverurl"
)

type Status string

const (
	StatusPass    Status = "pass"
	StatusWarn    Status = "warn"
	StatusFail    Status = "fail"
	StatusSkipped Status = "skipped"
)

type Check struct {
	Name    string         `json:"name"`
	Status  Status         `json:"status"`
	Message string         `json:"message"`
	Details map[string]any `json:"details,omitempty"`
}

type Report struct {
	OK     bool    `json:"ok"`
	Checks []Check `json:"checks"`
}

// Transport is the complete network authority required by doctor.
type Transport interface {
	GetPublic(context.Context, string, any) error
	Get(context.Context, string, any) error
}

// Factory binds doctor's transport authority to the normalized resolved URL and
// exact resolved credential. The returned close function releases that authority.
type Factory func(baseURL, token string) (Transport, func(), error)

const checkTimeout = 10 * time.Second

func Run(ctx context.Context, resolved config.Resolved, factory Factory) Report {
	checks := make([]Check, 0, 3)
	checks = append(checks, configurationCheck(resolved))

	normalizedURL, urlErr := serverurl.Normalize(resolved.URL)
	var transport Transport
	var factoryErr error
	closeTransport := func() {}
	if resolved.URL != "" && urlErr == nil {
		if factory == nil {
			factoryErr = errors.New("transport factory unavailable")
		} else {
			transport, closeTransport, factoryErr = factory(normalizedURL, resolved.Token)
			if closeTransport == nil {
				closeTransport = func() {}
			}
		}
	}
	defer closeTransport()
	if resolved.URL == "" {
		checks = append(checks, Check{Name: "server", Status: StatusSkipped, Message: "Mattermost URL is missing"})
	} else if urlErr != nil {
		checks = append(checks, Check{Name: "server", Status: StatusFail, Message: "Mattermost URL is invalid or unsafe"})
	} else {
		var ping pingResponse
		requestCtx, cancel := context.WithTimeout(ctx, checkTimeout)
		err := factoryErr
		if err == nil {
			err = transportError(transport, func(t Transport) error {
				return t.GetPublic(requestCtx, "/system/ping?get_server_status=true", &ping)
			})
		}
		cancel()
		checks = append(checks, serverCheck(resolved, ping, err))
	}

	if resolved.Token == "" {
		checks = append(checks, Check{Name: "authentication", Status: StatusSkipped, Message: "Mattermost token is missing"})
	} else if resolved.URL == "" || urlErr != nil {
		checks = append(checks, Check{Name: "authentication", Status: StatusSkipped, Message: "valid Mattermost URL is missing"})
	} else {
		var identity identityResponse
		requestCtx, cancel := context.WithTimeout(ctx, checkTimeout)
		err := factoryErr
		if err == nil {
			err = transportError(transport, func(t Transport) error {
				return t.Get(requestCtx, "/users/me", &identity)
			})
		}
		cancel()
		checks = append(checks, authenticationCheck(resolved, identity, err))
	}

	ok := true
	for _, check := range checks {
		if check.Status == StatusFail {
			ok = false
		}
	}
	return Report{OK: ok, Checks: checks}
}

func configurationCheck(resolved config.Resolved) Check {
	details := map[string]any{"urlSource": resolved.URLSource, "tokenSource": resolved.TokenSource}
	if resolved.File.Error != "" || resolved.File.Unsafe != "" {
		return Check{Name: "configuration", Status: StatusFail, Message: "config file could not be loaded", Details: details}
	}
	if resolved.File.InsecurePermissions && resolved.File.Config.Token != "" {
		return Check{Name: "configuration", Status: StatusFail, Message: "config file permissions expose a stored token; run chmod 600", Details: details}
	}
	if resolved.URL == "" || resolved.Token == "" {
		return Check{Name: "configuration", Status: StatusFail, Message: "configuration is incomplete", Details: details}
	}
	if resolved.File.InsecurePermissions {
		return Check{Name: "configuration", Status: StatusWarn, Message: "config file permissions are broader than recommended; run chmod 600", Details: details}
	}
	return Check{Name: "configuration", Status: StatusPass, Message: "credentials resolved", Details: details}
}

type remoteString struct {
	value string
	valid bool
}

func (s *remoteString) UnmarshalJSON(data []byte) error {
	var value string
	if json.Unmarshal(data, &value) == nil && value != "" {
		s.value, s.valid = value, true
	}
	return nil
}

type pingResponse struct {
	Status          remoteString `json:"status"`
	DatabaseStatus  remoteString `json:"database_status"`
	FilestoreStatus remoteString `json:"filestore_status"`
}

func serverCheck(resolved config.Resolved, ping pingResponse, err error) Check {
	if err != nil {
		return failedRequest("server", "server health request failed", err)
	}
	status := safeRemote(ping.Status, resolved)
	database := safeRemote(ping.DatabaseStatus, resolved)
	filestore := safeRemote(ping.FilestoreStatus, resolved)
	details := map[string]any{"status": status, "databaseStatus": database, "filestoreStatus": filestore}
	values := []string{status, database, filestore}
	for _, value := range values {
		if value != "OK" && value != "unknown" {
			return Check{Name: "server", Status: StatusFail, Message: "server reported an unhealthy component", Details: details}
		}
	}
	for _, value := range values {
		if value == "unknown" {
			return Check{Name: "server", Status: StatusWarn, Message: "server responded with incomplete health data", Details: details}
		}
	}
	return Check{Name: "server", Status: StatusPass, Message: "server is healthy", Details: details}
}

type identityResponse struct {
	ID       remoteString `json:"id"`
	Username remoteString `json:"username"`
}

func authenticationCheck(resolved config.Resolved, identity identityResponse, err error) Check {
	if err != nil {
		return failedRequest("authentication", "authentication request failed", err)
	}
	if !identity.ID.valid || strings.TrimSpace(identity.ID.value) == "" ||
		!identity.Username.valid || strings.TrimSpace(identity.Username.value) == "" {
		return Check{Name: "authentication", Status: StatusFail, Message: "authentication response was invalid"}
	}
	return Check{Name: "authentication", Status: StatusPass, Message: "authenticated", Details: map[string]any{
		"id": safeRemote(identity.ID, resolved), "username": safeRemote(identity.Username, resolved),
	}}
}

func safeRemote(value remoteString, resolved config.Resolved) string {
	if !value.valid {
		return "unknown"
	}
	processed := presentation.PreprocessWithOptions(value.value, presentation.Options{
		Credentials: []string{resolved.Token}, DisableHeuristics: !resolved.Redact,
	})
	return presentation.SanitizeLabel(processed.Text)
}

func failedRequest(name, message string, err error) Check {
	check := Check{Name: name, Status: StatusFail, Message: message}
	var apiError *api.APIError
	if errors.As(err, &apiError) && apiError.Status > 0 {
		check.Details = map[string]any{"httpStatus": apiError.Status}
	}
	return check
}

func transportError(transport Transport, request func(Transport) error) error {
	if transport == nil {
		return errors.New("transport unavailable")
	}
	return request(transport)
}
