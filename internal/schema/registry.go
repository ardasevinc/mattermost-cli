package schema

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"slices"
	"strings"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"

	publicschemas "github.com/ardasevinc/mattermost-cli/schemas"
)

const maxDocumentBytes = 4 << 20

// InputReadError identifies a physical failure while reading a validation
// document. JSON and schema validation errors deliberately do not use it.
type InputReadError struct{ err error }

func (e *InputReadError) Error() string { return "could not read JSON document" }
func (e *InputReadError) Unwrap() error { return e.err }

func IsInputReadError(err error) bool {
	var target *InputReadError
	return errors.As(err, &target)
}

type Registry struct {
	raw      map[string][]byte
	compiled map[string]*jsonschema.Schema
}

func Load() (*Registry, error) {
	paths, err := fs.Glob(publicschemas.FS, "v2/*.schema.json")
	if err != nil {
		return nil, fmt.Errorf("discover embedded schemas: %w", err)
	}
	compiler := jsonschema.NewCompiler()
	compiler.AssertFormat()
	raw := make(map[string][]byte, len(paths))
	for _, path := range paths {
		data, err := fs.ReadFile(publicschemas.FS, path)
		if err != nil {
			return nil, fmt.Errorf("read embedded schema: %w", err)
		}
		var document map[string]any
		if err := json.Unmarshal(data, &document); err != nil {
			return nil, fmt.Errorf("decode embedded schema %s: %w", path, err)
		}
		resourceID, ok := document["$id"].(string)
		if !ok || !strings.HasPrefix(resourceID, "urn:mm:schema:v2:") {
			return nil, fmt.Errorf("embedded schema %s has invalid identifier", path)
		}
		logicalID, ok := logicalIdentifier(document)
		if !ok {
			return nil, fmt.Errorf("embedded schema %s has invalid logical identifier", path)
		}
		if _, duplicate := raw[logicalID]; duplicate {
			return nil, fmt.Errorf("duplicate embedded schema identifier")
		}
		if err := compiler.AddResource(resourceID, document); err != nil {
			return nil, fmt.Errorf("register embedded schema: %w", err)
		}
		raw[logicalID] = data
	}
	compiled := make(map[string]*jsonschema.Schema, len(raw))
	for logicalID, data := range raw {
		var document map[string]any
		if err := json.Unmarshal(data, &document); err != nil {
			return nil, fmt.Errorf("decode embedded schema: %w", err)
		}
		resourceID, _ := document["$id"].(string)
		compiled[logicalID], err = compiler.Compile(resourceID)
		if err != nil {
			return nil, fmt.Errorf("compile embedded schema: %w", err)
		}
	}
	return &Registry{raw: raw, compiled: compiled}, nil
}

func (r *Registry) IDs() []string {
	ids := make([]string, 0, len(r.raw))
	for id := range r.raw {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}

func (r *Registry) Show(id string) ([]byte, error) {
	data, ok := r.raw[id]
	if !ok {
		return nil, fmt.Errorf("unknown schema")
	}
	return bytes.Clone(data), nil
}

func (r *Registry) Validate(id string, input io.Reader) error {
	compiled, ok := r.compiled[id]
	if !ok {
		return fmt.Errorf("unknown schema")
	}
	limited := io.LimitReader(input, maxDocumentBytes+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return &InputReadError{err: err}
	}
	if len(data) > maxDocumentBytes {
		return fmt.Errorf("JSON document exceeds %d bytes", maxDocumentBytes)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var document any
	if err := decoder.Decode(&document); err != nil {
		return fmt.Errorf("decode JSON document: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("decode JSON document: trailing JSON value")
		}
		return fmt.Errorf("decode JSON document trailer: %w", err)
	}
	if err := compiled.Validate(document); err != nil {
		return fmt.Errorf("document does not match %s", id)
	}
	return nil
}

func logicalIdentifier(document map[string]any) (string, bool) {
	properties, ok := document["properties"].(map[string]any)
	if !ok {
		return "", false
	}
	schemaProperty, ok := properties["schema"].(map[string]any)
	if !ok {
		return "", false
	}
	logicalID, ok := schemaProperty["const"].(string)
	return logicalID, ok && strings.HasPrefix(logicalID, "mm/v2/")
}
