package bmkgimport

import "testing"

func TestCursorGrammar(t *testing.T) {
	const sha = "abc123def456"
	cases := []struct {
		name      string
		cursor    string
		wantPhase Phase
		wantRow   int
		wantPoll  string
	}{
		{"empty cursor starts importing from row 0", "", PhaseImporting, 0, ""},
		{"M1-style plain counter is not a catalog cursor at all", "7", PhaseImporting, 0, ""},
		{"resume import at a specific row", "catalog:" + sha + ":42", PhaseImporting, 42, ""},
		{"import already finished, resume polling", "catalog:" + sha + ":done:5", PhasePolling, 0, "5"},
		{"import finished with poll counter 0", "catalog:" + sha + ":done:0", PhasePolling, 0, "0"},
		{"mismatched hash restarts the import from 0", "catalog:different-hash:42", PhaseImporting, 0, ""},
		{"mismatched hash on a done cursor also restarts", "catalog:different-hash:done:5", PhaseImporting, 0, ""},
		{"malformed row number restarts the import", "catalog:" + sha + ":not-a-number", PhaseImporting, 0, ""},
		{"negative row number restarts the import", "catalog:" + sha + ":-1", PhaseImporting, 0, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			phase, row, poll := Parse(c.cursor, sha)
			if phase != c.wantPhase || row != c.wantRow || poll != c.wantPoll {
				t.Fatalf("Parse(%q, %q) = (%v, %d, %q), want (%v, %d, %q)", c.cursor, sha, phase, row, poll, c.wantPhase, c.wantRow, c.wantPoll)
			}
		})
	}
}
