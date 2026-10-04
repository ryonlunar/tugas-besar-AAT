package contracts

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

//go:embed hazard-event.schema.json hazard-envelope.schema.json
var envelopeSchemas embed.FS

var (
	envelopeOnce   sync.Once
	envelopeSchema *jsonschema.Schema
	envelopeErr    error
)

// Envelope is the wire message accepted by the independent consumers.
type Envelope struct {
	SchemaVersion int             `json:"schema_version"`
	EventType     string          `json:"event_type"`
	MessageID     string          `json:"message_id"`
	CorrelationID string          `json:"correlation_id"`
	PublishedAt   string          `json:"published_at"`
	Producer      string          `json:"producer"`
	Revision      int64           `json:"revision"`
	Data          json.RawMessage `json:"data"`
	HazardID      string          `json:"-"`
	Source        string          `json:"-"`
}

func compileEnvelope() {
	compiler := jsonschema.NewCompiler()
	compiler.AssertFormat()
	for _, name := range []string{"hazard-event.schema.json", "hazard-envelope.schema.json"} {
		raw, err := envelopeSchemas.ReadFile(name)
		if err != nil {
			envelopeErr = err
			return
		}
		var document any
		if err := json.Unmarshal(raw, &document); err != nil {
			envelopeErr = err
			return
		}
		if err := compiler.AddResource("https://schemas.aat.invalid/"+name, document); err != nil {
			envelopeErr = err
			return
		}
	}
	envelopeSchema, envelopeErr = compiler.Compile("https://schemas.aat.invalid/hazard-envelope.schema.json")
}

// ParseEnvelope validates the shared JSON schema and the relationships that JSON
// Schema cannot express across fields. The caller should quarantine invalid data.
func ParseEnvelope(raw []byte, subject, headerMessageID string) (Envelope, error) {
	envelopeOnce.Do(compileEnvelope)
	if envelopeErr != nil {
		return Envelope{}, fmt.Errorf("compile envelope schema: %w", envelopeErr)
	}
	var document any
	if err := json.Unmarshal(raw, &document); err != nil {
		return Envelope{}, fmt.Errorf("decode envelope: %w", err)
	}
	if err := envelopeSchema.Validate(document); err != nil {
		return Envelope{}, fmt.Errorf("invalid envelope: %w", err)
	}
	var result Envelope
	if err := json.Unmarshal(raw, &result); err != nil {
		return Envelope{}, err
	}
	var data struct {
		HazardID string `json:"hazard_id"`
		Source   string `json:"source"`
	}
	if err := json.Unmarshal(result.Data, &data); err != nil {
		return Envelope{}, err
	}
	if result.MessageID != fmt.Sprintf("%s:%d", data.HazardID, result.Revision) {
		return Envelope{}, errors.New("message_id does not match hazard_id and revision")
	}
	if headerMessageID != "" && headerMessageID != result.MessageID {
		return Envelope{}, errors.New("Nats-Msg-Id does not match message_id")
	}
	if subject != "hazard.events.v1."+data.Source || strings.Contains(subject, " ") {
		return Envelope{}, errors.New("subject does not match event source")
	}
	result.HazardID, result.Source = data.HazardID, data.Source
	return result, nil
}
