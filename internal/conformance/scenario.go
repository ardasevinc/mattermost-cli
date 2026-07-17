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

const (
	SchemaV1     = "mm/conformance/v1"
	PairSchemaV1 = "mm/conformance-pair/v1"
)

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

type PairScenario struct {
	Schema    string   `json:"schema"`
	Name      string   `json:"name"`
	Oracle    Scenario `json:"oracle"`
	Candidate Scenario `json:"candidate"`
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
	var scenario Scenario
	if err := decodeFile(path, &scenario); err != nil {
		return Scenario{}, fmt.Errorf("decode scenario: %w", err)
	}
	if err := validateScenario(scenario, true); err != nil {
		return Scenario{}, err
	}
	return scenario, nil
}

func LoadPair(path string) (PairScenario, error) {
	var pair PairScenario
	if err := decodeFile(path, &pair); err != nil {
		return PairScenario{}, fmt.Errorf("decode pair scenario: %w", err)
	}
	if pair.Schema != PairSchemaV1 {
		return PairScenario{}, fmt.Errorf("unsupported pair scenario schema %q", pair.Schema)
	}
	if pair.Name == "" {
		return PairScenario{}, fmt.Errorf("pair scenario name is required")
	}
	if err := validateScenario(pair.Oracle, false); err != nil {
		return PairScenario{}, fmt.Errorf("oracle: %w", err)
	}
	if err := validateScenario(pair.Candidate, false); err != nil {
		return PairScenario{}, fmt.Errorf("candidate: %w", err)
	}
	return pair, nil
}

func decodeFile(path string, destination any) error {
	f, err := os.Open(path) // #nosec G304 -- operator-selected local scenario fixture.
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	decoder := json.NewDecoder(f)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("trailing JSON value")
		}
		return fmt.Errorf("decode trailer: %w", err)
	}
	return nil
}

func validateScenario(scenario Scenario, requireHeader bool) error {
	if requireHeader && scenario.Schema != SchemaV1 {
		return fmt.Errorf("unsupported scenario schema %q", scenario.Schema)
	}
	if !requireHeader && scenario.Schema != "" {
		return fmt.Errorf("nested scenario must not declare schema")
	}
	if requireHeader && scenario.Name == "" {
		return fmt.Errorf("scenario name is required")
	}
	if !requireHeader && scenario.Name != "" {
		return fmt.Errorf("nested scenario must not declare name")
	}
	if scenario.Args == nil {
		return fmt.Errorf("scenario args are required")
	}
	if scenario.Expected == nil || scenario.Expected.ExitCode == nil || scenario.Expected.Stdout == nil || scenario.Expected.Stderr == nil {
		return fmt.Errorf("scenario expected exitCode, stdout, and stderr are required")
	}
	if *scenario.Expected.ExitCode < 0 || *scenario.Expected.ExitCode > 255 {
		return fmt.Errorf("scenario expected exitCode must be between 0 and 255")
	}
	if scenario.Timeout != nil && (*scenario.Timeout < 1 || *scenario.Timeout > 300_000) {
		return fmt.Errorf("scenario timeoutMs must be between 1 and 300000 when set")
	}
	for name := range scenario.Env {
		if !envNamePattern.MatchString(name) {
			return fmt.Errorf("invalid scenario environment name %q", name)
		}
	}
	for i, exchange := range scenario.HTTP {
		if exchange.Request.Method == "" || exchange.Request.URI == "" {
			return fmt.Errorf("http exchange %d requires request method and uri", i)
		}
		if exchange.Response.Status < 100 || exchange.Response.Status > 599 {
			return fmt.Errorf("http exchange %d has invalid response status", i)
		}
		for _, name := range exchange.Request.IgnoreHeaders {
			switch http.CanonicalHeaderKey(name) {
			case "Authorization", "Cookie", "Proxy-Authorization":
				return fmt.Errorf("http exchange %d cannot ignore security-sensitive header %q", i, name)
			}
		}
	}
	return nil
}
