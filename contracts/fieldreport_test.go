package contracts

import (
	"os"
	"strings"
	"testing"
)

func TestValidateFieldReport(t *testing.T) {
	example, err := os.ReadFile("examples/field-report.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateFieldReport(example); err != nil {
		t.Fatalf("example rejected: %v", err)
	}
	valid := `{"observed_at":"2026-11-12T03:00:00Z","latitude":-1,"longitude":100,"description":"x"}`
	cases := map[string]string{
		"truncated":      valid[:len(valid)-3],
		"extra field":    strings.Replace(valid, `"description"`, `"role":"admin","description"`, 1),
		"missing field":  `{"observed_at":"2026-11-12T03:00:00Z","latitude":-1,"longitude":100}`,
		"null field":     strings.Replace(valid, `"x"`, `null`, 1),
		"bad time":       strings.Replace(valid, "2026-11-12T03:00:00Z", "yesterday", 1),
		"latitude range": strings.Replace(valid, `-1`, `-91`, 1),
		"longitude type": strings.Replace(valid, `100`, `"100"`, 1),
		"empty text":     strings.Replace(valid, `"x"`, `""`, 1),
		"long text":      strings.Replace(valid, `"x"`, `"`+strings.Repeat("a", 4097)+`"`, 1),
		"trailing value": valid + `{}`,
		"invalid utf8":   strings.Replace(valid, "x", "\xff", 1),
		"escaped nul":    strings.Replace(valid, `"x"`, `"a\u0000b"`, 1),
		"array":          `[]`,
		"oversized":      strings.Repeat(" ", MaxFieldReportBytes+1),
	}
	for name, body := range cases {
		if _, err := ValidateFieldReport([]byte(body)); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if _, err := ValidateFieldReport([]byte(strings.Replace(valid, `"x"`, `"`+strings.Repeat("é", 4096)+`"`, 1))); err != nil {
		t.Fatalf("4096 multi-byte characters rejected: %v", err)
	}
}

func TestIdempotencyKeyPattern(t *testing.T) {
	for _, key := range []string{"a", "report:2026-11-12_01.a-b", strings.Repeat("k", 128)} {
		if !IdempotencyKeyPattern.MatchString(key) {
			t.Errorf("%q rejected", key)
		}
	}
	for _, key := range []string{"", "has space", "slash/key", strings.Repeat("k", 129)} {
		if IdempotencyKeyPattern.MatchString(key) {
			t.Errorf("%q accepted", key)
		}
	}
}
