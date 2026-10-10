// Validate the actual canonical records captured by the integration trial.
package main

import (
	"encoding/json"
	"fmt"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"os"
)

func main() {
	compiler := jsonschema.NewCompiler()
	compiler.AssertFormat()
	raw, err := os.ReadFile("contracts/hazard-event.schema.json")
	must(err)
	var doc map[string]any
	must(json.Unmarshal(raw, &doc))
	id := doc["$id"].(string)
	must(compiler.AddResource(id, doc))
	schema, err := compiler.Compile(id)
	must(err)
	raw, err = os.ReadFile("docs/report/evidence/integration.json")
	must(err)
	var trial struct {
		Results []struct {
			Check     string `json:"check"`
			Canonical any    `json:"canonical_record"`
			Audit     struct {
				Versions []struct {
					Revision int
					Data     any
				}
			}
		}
	}
	must(json.Unmarshal(raw, &trial))
	count := 0
	for _, result := range trial.Results {
		if result.Canonical != nil {
			must(schema.Validate(result.Canonical))
			fmt.Printf("%s: HazardEvent schema valid\n", result.Check)
			count++
		}
		for _, version := range result.Audit.Versions {
			must(schema.Validate(version.Data))
			fmt.Printf("PVMBG revision %d: HazardEvent schema valid\n", version.Revision)
			count++
		}
	}
	raw, err = os.ReadFile("docs/report/evidence/mapping-command-records.json")
	must(err)
	var records map[string]any
	must(json.Unmarshal(raw, &records))
	for _, name := range []string{"p1-bmkg-canonical", "p1-pvmbg-canonical"} {
		must(schema.Validate(records[name]))
		fmt.Printf("7 Oktober, %s: HazardEvent schema valid\n", name)
		count++
	}
	raw, err = os.ReadFile("docs/report/evidence/schema-command-audit.json")
	must(err)
	var audit struct {
		Versions []struct {
			Revision int
			Data     any
		}
	}
	must(json.Unmarshal(raw, &audit))
	for _, version := range audit.Versions {
		must(schema.Validate(version.Data))
		fmt.Printf("7 Oktober, PVMBG revision %d: HazardEvent schema valid\n", version.Revision)
		count++
	}
	if count != 8 {
		panic(fmt.Sprintf("expected 8 captured records, got %d", count))
	}
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}
