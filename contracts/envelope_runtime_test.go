package contracts

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"
)

func TestParseEnvelopeChecksWireAndCrossFieldRules(t *testing.T) {
	raw, err := os.ReadFile("examples/hazard-envelope.json")
	if err != nil {
		t.Fatal(err)
	}
	good, err := ParseEnvelope(raw, "hazard.events.v1.BMKG", "31b67234-7363-4fba-9c20-d7f2b6d91450:1")
	if err != nil || good.HazardID != "31b67234-7363-4fba-9c20-d7f2b6d91450" {
		t.Fatalf("valid producer envelope rejected: %v", err)
	}
	for name, change := range map[string]func(map[string]any){
		"id mismatch":   func(m map[string]any) { m["message_id"] = "31b67234-7363-4fba-9c20-d7f2b6d91450:2" },
		"extra field":   func(m map[string]any) { m["unexpected"] = true },
		"invalid event": func(m map[string]any) { m["data"].(map[string]any)["source"] = "OTHER" },
	} {
		t.Run(name, func(t *testing.T) {
			var document map[string]any
			if err := json.Unmarshal(raw, &document); err != nil {
				t.Fatal(err)
			}
			change(document)
			payload, err := json.Marshal(document)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := ParseEnvelope(payload, "hazard.events.v1.BMKG", ""); err == nil {
				t.Fatal("invalid envelope accepted")
			}
		})
	}
	if _, err := ParseEnvelope(raw, "hazard.events.v1.PVMBG", ""); err == nil {
		t.Fatal("wrong subject accepted")
	}
	if _, err := ParseEnvelope(raw, "hazard.events.v1.BMKG", "wrong"); err == nil {
		t.Fatal("wrong header ID accepted")
	}
	if _, err := ParseEnvelope(bytes.Repeat([]byte("x"), 5), "hazard.events.v1.BMKG", ""); err == nil {
		t.Fatal("invalid JSON accepted")
	}
}
