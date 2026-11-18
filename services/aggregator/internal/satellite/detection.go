package satellite

import (
	"fmt"
	"strings"
	"time"
)

// DetectedAt computes the UTC detection time from a VIIRS-style acq_date
// ("2026-11-12") and acq_time ("243", "2357", or "0" -- a 24h clock value
// with no leading zeros guaranteed, matching what the feed's JSON number
// encoding can produce) per Definisi Kebakaran point 2 (FIRE.2): detection
// time is the satellite's own pass time, never the time BNPB received it.
//
// acqTime is zero-padded on the LEFT only for this time computation ("243"
// -> "0243" -> 02:43); the un-padded original token is preserved separately
// wherever a detection_key is built (Detection.Key, D8) so the key never
// contains a digit this function introduced.
func DetectedAt(acqDate, acqTime string) (time.Time, error) {
	digits := acqTime
	if len(digits) == 0 || len(digits) > 4 {
		return time.Time{}, fmt.Errorf("acq_time %q tidak valid: panjang di luar 1-4 digit", acqTime)
	}
	for _, r := range digits {
		if r < '0' || r > '9' {
			return time.Time{}, fmt.Errorf("acq_time %q tidak valid: bukan digit", acqTime)
		}
	}
	padded := strings.Repeat("0", 4-len(digits)) + digits
	hour := (padded[0]-'0')*10 + (padded[1] - '0')
	minute := (padded[2]-'0')*10 + (padded[3] - '0')
	if hour > 23 || minute > 59 {
		return time.Time{}, fmt.Errorf("acq_time %q tidak valid: jam/menit di luar rentang", acqTime)
	}
	t, err := time.Parse("2006-01-02 15:04", fmt.Sprintf("%s %02d:%02d", acqDate, hour, minute))
	if err != nil {
		return time.Time{}, fmt.Errorf("acq_date %q tidak valid: %w", acqDate, err)
	}
	return t.UTC(), nil
}

// Key returns the detection's identity per Definisi Kebakaran point 1 and
// A.events.5: satellite|acq_date|acq_time|latitude|longitude built from the
// exact lexical tokens the feed sent -- never a value that has passed
// through float64 (which could reformat or round a digit sequence). Two
// detections with the same Key are the same detection; repeated delivery of
// an identical Key is deduplicated by the recognizer (FIRE.1), not here.
func (d Detection) Key() string {
	return d.Satellite + "|" + d.AcqDate + "|" + d.AcqTimeText + "|" + d.LatText + "|" + d.LonText
}

// DetectedAt computes this detection's UTC detection time from its own
// AcqDate/AcqTimeText. It is a thin wrapper around the package-level
// DetectedAt so callers that already have a Detection do not need to thread
// the two string fields through by hand.
func (d Detection) DetectedAt() (time.Time, error) {
	return DetectedAt(d.AcqDate, d.AcqTimeText)
}
