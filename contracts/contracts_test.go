package contracts

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

func TestSchemasAndExamples(t *testing.T) {
	compiler := jsonschema.NewCompiler()
	compiler.AssertFormat()

	schemaFiles := []string{
		"hazard-event.schema.json",
		"hazard-summary.schema.json",
		"hazard-envelope.schema.json",
	}
	schemaIDs := make(map[string]string, len(schemaFiles))
	for _, filename := range schemaFiles {
		document := readJSON(t, filename)
		schemaDocument, ok := document.(map[string]any)
		if !ok {
			t.Fatalf("schema %s is not a JSON object", filename)
		}
		id, ok := schemaDocument["$id"].(string)
		if !ok || id == "" {
			t.Fatalf("schema %s has no non-empty $id", filename)
		}
		if err := compiler.AddResource(id, document); err != nil {
			t.Fatalf("register schema %s: %v", filename, err)
		}
		schemaIDs[filename] = id
	}

	schemas := make(map[string]*jsonschema.Schema, len(schemaFiles))
	for _, filename := range schemaFiles {
		compiled, err := compiler.Compile(schemaIDs[filename])
		if err != nil {
			t.Fatalf("compile schema %s: %v", filename, err)
		}
		schemas[filename] = compiled
	}

	examples := []struct {
		name     string
		schema   string
		fixture  string
		envelope bool
	}{
		{name: "seismic event", schema: "hazard-event.schema.json", fixture: "examples/seismic-hazard.json"},
		{name: "volcanic v1 event", schema: "hazard-event.schema.json", fixture: "examples/volcanic-v1-hazard.json"},
		{name: "volcanic v2 event", schema: "hazard-event.schema.json", fixture: "examples/volcanic-v2-hazard.json"},
		{name: "media summary", schema: "hazard-summary.schema.json", fixture: "examples/media-summary.json"},
		{name: "hazard envelope", schema: "hazard-envelope.schema.json", fixture: "examples/hazard-envelope.json", envelope: true},
	}
	for _, example := range examples {
		t.Run("accept/"+example.name, func(t *testing.T) {
			document := readJSON(t, example.fixture)
			if err := schemas[example.schema].Validate(document); err != nil {
				t.Fatalf("fixture %s rejected: %v", example.fixture, err)
			}
			if example.envelope {
				if err := validateEnvelopeRelationships(asObject(t, document)); err != nil {
					t.Fatalf("fixture %s violates cross-field relationships: %v", example.fixture, err)
				}
			}
		})
	}

	invalidCases := []struct {
		name    string
		schema  string
		fixture string
		mutate  func(map[string]any)
	}{
		{
			name: "event UUID format", schema: "hazard-event.schema.json", fixture: "examples/seismic-hazard.json",
			mutate: func(document map[string]any) { document["hazard_id"] = "not-a-uuid" },
		},
		{
			name: "date-time format", schema: "hazard-event.schema.json", fixture: "examples/seismic-hazard.json",
			mutate: func(document map[string]any) { document["occurred_at"] = "not-a-date-time" },
		},
		{
			name: "source reference character contract", schema: "hazard-event.schema.json", fixture: "examples/seismic-hazard.json",
			mutate: func(document map[string]any) { document["source_ref_id"] = "source id with spaces" },
		},
		{
			name: "source and hazard type relationship", schema: "hazard-event.schema.json", fixture: "examples/seismic-hazard.json",
			mutate: func(document map[string]any) { document["hazard_type"] = "VOLCANIC" },
		},
		{
			name: "summary UUID format", schema: "hazard-summary.schema.json", fixture: "examples/media-summary.json",
			mutate: func(document map[string]any) { document["hazard_id"] = "bad-id" },
		},
		{
			name: "message ID shape", schema: "hazard-envelope.schema.json", fixture: "examples/hazard-envelope.json",
			mutate: func(document map[string]any) { document["message_id"] = "bad-id:1" },
		},
		{
			name: "event type and revision relationship", schema: "hazard-envelope.schema.json", fixture: "examples/hazard-envelope.json",
			mutate: func(document map[string]any) { document["event_type"] = "hazard.updated" },
		},
	}
	for _, testCase := range invalidCases {
		t.Run("reject/schema/"+testCase.name, func(t *testing.T) {
			document := cloneObject(t, readJSON(t, testCase.fixture))
			testCase.mutate(document)
			if err := schemas[testCase.schema].Validate(document); err == nil {
				t.Fatalf("schema %s accepted invalid case %q", testCase.schema, testCase.name)
			}
		})
	}

	t.Run("reject/relationship/message hazard ID mismatch", func(t *testing.T) {
		document := cloneObject(t, readJSON(t, "examples/hazard-envelope.json"))
		document["message_id"] = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa:1"
		if err := schemas["hazard-envelope.schema.json"].Validate(document); err != nil {
			t.Fatalf("mismatched hazard ID should pass the schema before the cross-field check: %v", err)
		}
		if err := validateEnvelopeRelationships(document); err == nil {
			t.Fatal("cross-field check accepted a message_id for a different hazard")
		}
	})

	t.Run("reject/relationship/message revision mismatch", func(t *testing.T) {
		document := cloneObject(t, readJSON(t, "examples/hazard-envelope.json"))
		document["revision"] = 2
		document["event_type"] = "hazard.updated"
		data := asObject(t, document["data"])
		document["message_id"] = fmt.Sprint(data["hazard_id"]) + ":1"
		if err := schemas["hazard-envelope.schema.json"].Validate(document); err != nil {
			t.Fatalf("mismatched revision should pass the schema before the cross-field check: %v", err)
		}
		if err := validateEnvelopeRelationships(document); err == nil {
			t.Fatal("cross-field check accepted a message_id with a different revision")
		}
	})
}

func readJSON(t *testing.T, filename string) any {
	t.Helper()
	data, err := os.ReadFile(filename)
	if err != nil {
		t.Fatalf("read %s: %v", filename, err)
	}
	document, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("parse %s: %v", filename, err)
	}
	return document
}

func cloneObject(t *testing.T, document any) map[string]any {
	t.Helper()
	encoded, err := json.Marshal(document)
	if err != nil {
		t.Fatalf("clone JSON document: %v", err)
	}
	cloned, err := jsonschema.UnmarshalJSON(bytes.NewReader(encoded))
	if err != nil {
		t.Fatalf("parse cloned JSON document: %v", err)
	}
	return asObject(t, cloned)
}

func asObject(t *testing.T, value any) map[string]any {
	t.Helper()
	object, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("value has type %T, want JSON object", value)
	}
	return object
}

func validateEnvelopeRelationships(document map[string]any) error {
	messageID, ok := document["message_id"].(string)
	if !ok {
		return fmt.Errorf("message_id is not a string")
	}
	separator := strings.LastIndexByte(messageID, ':')
	if separator < 0 {
		return fmt.Errorf("message_id has no revision suffix")
	}
	messageHazardID := messageID[:separator]
	messageRevision, err := strconv.ParseInt(messageID[separator+1:], 10, 64)
	if err != nil {
		return fmt.Errorf("parse message_id revision: %w", err)
	}

	data, ok := document["data"].(map[string]any)
	if !ok {
		return fmt.Errorf("data is not an object")
	}
	hazardID, ok := data["hazard_id"].(string)
	if !ok {
		return fmt.Errorf("data.hazard_id is not a string")
	}
	if messageHazardID != hazardID {
		return fmt.Errorf("message_id hazard UUID must equal data.hazard_id")
	}

	revision, err := strconv.ParseInt(fmt.Sprint(document["revision"]), 10, 64)
	if err != nil {
		return fmt.Errorf("parse revision: %w", err)
	}
	if messageRevision != revision {
		return fmt.Errorf("message_id revision suffix must equal revision")
	}
	return nil
}
