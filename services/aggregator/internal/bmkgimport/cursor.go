package bmkgimport

import (
	"strconv"
	"strings"
)

// Phase identifies which stage of BMKG's historical-catalog import a cursor
// represents (D11).
type Phase int

const (
	// PhaseImporting means rows up to (but not including) Row have already
	// been imported; the adapter should resume at Row.
	PhaseImporting Phase = iota
	// PhasePolling means the catalog import finished; the adapter should
	// return io.EOF immediately so PollingAdapter's live polling takes
	// over, resuming its own counter from Poll.
	PhasePolling
)

// Parse interprets a BMKG source cursor against the catalog file's current
// content hash (sha12 -- typically the first 12 hex characters of
// sha256(file bytes)).
//
//   - "" or any cursor without a "catalog:" prefix (M1's own plain polling
//     counter, e.g. "7", or simply no prior progress at all) means no
//     catalog import has ever run for this file: PhaseImporting, Row 0.
//   - "catalog:<sha>:<row>" with sha matching the current file means:
//     resume importing from Row.
//   - "catalog:<sha>:done:<poll>" with sha matching means: import already
//     finished; PhasePolling, resume the live poll counter from Poll.
//   - Any cursor whose sha does NOT match sha12 -- the file's content
//     changed since the recorded progress -- is treated exactly like no
//     progress at all: PhaseImporting, Row 0 (D11: a changed file's old
//     progress means nothing, so it must not be trusted).
func Parse(cursor, sha12 string) (phase Phase, row int, poll string) {
	rest, ok := strings.CutPrefix(cursor, "catalog:")
	if !ok {
		return PhaseImporting, 0, ""
	}
	sha, tail, ok := strings.Cut(rest, ":")
	if !ok || sha != sha12 {
		return PhaseImporting, 0, ""
	}
	if pollCounter, ok := strings.CutPrefix(tail, "done:"); ok {
		return PhasePolling, 0, pollCounter
	}
	r, err := strconv.Atoi(tail)
	if err != nil || r < 0 {
		return PhaseImporting, 0, ""
	}
	return PhaseImporting, r, ""
}
