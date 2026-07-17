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
	"unicode/utf8"

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
	_, err := r.ReadAndValidate(id, input)
	return err
}

// ReadAndValidate consumes one bounded JSON document, validates it, and returns
// the exact bytes consumed so callers can decode the already-validated input
// without reading a potentially non-repeatable source twice.
func (r *Registry) ReadAndValidate(id string, input io.Reader) ([]byte, error) {
	compiled, ok := r.compiled[id]
	if !ok {
		return nil, fmt.Errorf("unknown schema")
	}
	limited := io.LimitReader(input, maxDocumentBytes+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, &InputReadError{err: err}
	}
	if len(data) > maxDocumentBytes {
		return nil, fmt.Errorf("JSON document exceeds %d bytes", maxDocumentBytes)
	}
	if !utf8.Valid(data) {
		return nil, fmt.Errorf("decode JSON document: invalid UTF-8")
	}
	if err := rejectUnpairedSurrogateEscapes(data); err != nil {
		return nil, fmt.Errorf("decode JSON document: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var document any
	if err := decoder.Decode(&document); err != nil {
		return nil, fmt.Errorf("decode JSON document: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("decode JSON document: trailing JSON value")
		}
		return nil, fmt.Errorf("decode JSON document trailer: %w", err)
	}
	if err := compiled.Validate(document); err != nil {
		return nil, fmt.Errorf("document does not match %s", id)
	}
	return bytes.Clone(data), nil
}

// encoding/json replaces unpaired UTF-16 surrogate escapes with U+FFFD. Reject
// them first so validation never changes the meaning of caller-controlled text.
func rejectUnpairedSurrogateEscapes(data []byte) error {
	for i := 0; i < len(data); i++ {
		if data[i] != '"' {
			continue
		}
		for i++; i < len(data) && data[i] != '"'; i++ {
			if data[i] != '\\' {
				continue
			}
			i++
			if i >= len(data) {
				return nil // the JSON decoder reports the syntax error
			}
			if data[i] != 'u' || i+4 >= len(data) {
				continue
			}
			value, ok := hexQuad(data[i+1 : i+5])
			if !ok {
				continue
			}
			i += 4
			if value >= 0xdc00 && value <= 0xdfff {
				return fmt.Errorf("unpaired UTF-16 surrogate escape")
			}
			if value < 0xd800 || value > 0xdbff {
				continue
			}
			if i+6 >= len(data) || data[i+1] != '\\' || data[i+2] != 'u' {
				return fmt.Errorf("unpaired UTF-16 surrogate escape")
			}
			low, ok := hexQuad(data[i+3 : i+7])
			if !ok || low < 0xdc00 || low > 0xdfff {
				return fmt.Errorf("unpaired UTF-16 surrogate escape")
			}
			i += 6
		}
	}
	return nil
}

func hexQuad(value []byte) (uint16, bool) {
	var out uint16
	for _, c := range value {
		out <<= 4
		switch {
		case c >= '0' && c <= '9':
			out |= uint16(c - '0')
		case c >= 'a' && c <= 'f':
			out |= uint16(c-'a') + 10
		case c >= 'A' && c <= 'F':
			out |= uint16(c-'A') + 10
		default:
			return 0, false
		}
	}
	return out, true
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
