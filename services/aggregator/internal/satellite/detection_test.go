package satellite

import "testing"

func TestDetectedAtUTC(t *testing.T) {
	cases := []struct {
		name              string
		acqDate, acqTime  string
		wantHour, wantMin int
		wantErr           bool
	}{
		{name: "three-digit token zero-pads from the left", acqDate: "2026-11-12", acqTime: "243", wantHour: 2, wantMin: 43},
		{name: "single-digit token is 00:0X", acqDate: "2026-11-12", acqTime: "5", wantHour: 0, wantMin: 5},
		{name: "four-digit token with no padding needed", acqDate: "2026-11-20", acqTime: "2357", wantHour: 23, wantMin: 57},
		{name: "midnight", acqDate: "2026-01-01", acqTime: "0", wantHour: 0, wantMin: 0},
		{name: "hour 24 is invalid", acqDate: "2026-11-12", acqTime: "2400", wantErr: true},
		{name: "minute 60 is invalid", acqDate: "2026-11-12", acqTime: "160", wantErr: true}, // "0160" -> 01:60
		{name: "impossible calendar date is invalid", acqDate: "2026-02-30", acqTime: "0", wantErr: true},
		{name: "non-numeric token is invalid", acqDate: "2026-11-12", acqTime: "12a3", wantErr: true},
		{name: "five-digit token is too long", acqDate: "2026-11-12", acqTime: "12345", wantErr: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := DetectedAt(c.acqDate, c.acqTime)
			if c.wantErr {
				if err == nil {
					t.Fatalf("DetectedAt(%q, %q) = %v, want error", c.acqDate, c.acqTime, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("DetectedAt(%q, %q) unexpected error: %v", c.acqDate, c.acqTime, err)
			}
			if got.Hour() != c.wantHour || got.Minute() != c.wantMin {
				t.Fatalf("DetectedAt(%q, %q) = %02d:%02d, want %02d:%02d", c.acqDate, c.acqTime, got.Hour(), got.Minute(), c.wantHour, c.wantMin)
			}
			if got.Location().String() != "UTC" {
				t.Fatalf("DetectedAt(%q, %q) location = %v, want UTC", c.acqDate, c.acqTime, got.Location())
			}
		})
	}
}

func TestDetectionKeyPreservesLexicalTokens(t *testing.T) {
	d := Detection{Satellite: "N20", AcqDate: "2026-11-12", AcqTimeText: "243", LatText: "-2.51230", LonText: "113.80440"}
	want := "N20|2026-11-12|243|-2.51230|113.80440"
	if got := d.Key(); got != want {
		t.Fatalf("Key() = %q, want %q (must preserve the original digit sequence, e.g. trailing zero in -2.51230)", got, want)
	}

	// A detection whose latitude the feed sent with a different number of
	// trailing digits must produce a different key -- rounding or
	// reformatting through float64 would silently collapse them.
	other := Detection{Satellite: "N20", AcqDate: "2026-11-12", AcqTimeText: "243", LatText: "-2.5123", LonText: "113.80440"}
	if d.Key() == other.Key() {
		t.Fatalf("Key() must distinguish -2.51230 from -2.5123 (different lexical tokens)")
	}
}
