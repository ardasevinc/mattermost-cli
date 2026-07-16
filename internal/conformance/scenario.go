package conformance

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
)

const SchemaV1 = "mm/conformance/v1"

var envNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

type Scenario struct {
	Schema   string            `json:"schema"`
	Name     string            `json:"name"`
	Args     []string          `json:"args"`
	Stdin    string            `json:"stdin,omitempty"`
	Timeout  *int              `json:"timeoutMs,omitempty"`
	Env      map[string]string `json:"env,omitempty"`
	HTTP     []HTTPExchange    `json:"http,omitempty"`
	Expected *ProcessExpected  `json:"expected"`
}

type HTTPExchange struct {
	Request  HTTPRequestExpected `json:"request"`
	Response HTTPResponse        `json:"response"`
}

type HTTPRequestExpected struct {
	Method        string            `json:"method"`
	URI           string            `json:"uri"`
	Headers       map[string]string `json:"headers,omitempty"`
	IgnoreHeaders []string          `json:"ignoreHeaders,omitempty"`
	Body          string            `json:"body,omitempty"`
}

type HTTPResponse struct {
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    string            `json:"body,omitempty"`
}

type ProcessExpected struct {
	ExitCode *int    `json:"exitCode"`
	Stdout   *string `json:"stdout"`
	Stderr   *string `json:"stderr"`
}

func Load(path string) (Scenario, error) {
	f, err := os.Open(path) // #nosec G304 -- operator-selected local scenario fixture.
	if err != nil {
		return Scenario{}, err
	}
	defer func() { _ = f.Close() }()

	decoder := json.NewDecoder(f)
	decoder.DisallowUnknownFields()
	var scenario Scenario
	if err := decoder.Decode(&scenario); err != nil {
		return Scenario{}, fmt.Errorf("decode scenario: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return Scenario{}, fmt.Errorf("decode scenario: trailing JSON value")
		}
		return Scenario{}, fmt.Errorf("decode scenario trailer: %w", err)
	}
	if scenario.Schema != SchemaV1 {
		return Scenario{}, fmt.Errorf("unsupported scenario schema %q", scenario.Schema)
	}
	if scenario.Name == "" {
		return Scenario{}, fmt.Errorf("scenario name is required")
	}
	if scenario.Args == nil {
		return Scenario{}, fmt.Errorf("scenario args are required")
	}
	if scenario.Expected == nil || scenario.Expected.ExitCode == nil || scenario.Expected.Stdout == nil || scenario.Expected.Stderr == nil {
		return Scenario{}, fmt.Errorf("scenario expected exitCode, stdout, and stderr are required")
	}
	if *scenario.Expected.ExitCode < 0 || *scenario.Expected.ExitCode > 255 {
		return Scenario{}, fmt.Errorf("scenario expected exitCode must be between 0 and 255")
	}
	if scenario.Timeout != nil && (*scenario.Timeout < 1 || *scenario.Timeout > 300_000) {
		return Scenario{}, fmt.Errorf("scenario timeoutMs must be between 1 and 300000 when set")
	}
	for name := range scenario.Env {
		if !envNamePattern.MatchString(name) {
			return Scenario{}, fmt.Errorf("invalid scenario environment name %q", name)
		}
	}
	for i, exchange := range scenario.HTTP {
		if exchange.Request.Method == "" || exchange.Request.URI == "" {
			return Scenario{}, fmt.Errorf("http exchange %d requires request method and uri", i)
		}
		if exchange.Response.Status < 100 || exchange.Response.Status > 599 {
			return Scenario{}, fmt.Errorf("http exchange %d has invalid response status", i)
		}
		for _, name := range exchange.Request.IgnoreHeaders {
			switch http.CanonicalHeaderKey(name) {
			case "Authorization", "Cookie", "Proxy-Authorization":
				return Scenario{}, fmt.Errorf("http exchange %d cannot ignore security-sensitive header %q", i, name)
			}
		}
	}
	return scenario, nil
}
