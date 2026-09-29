package store

import (
	"testing"

	"github.com/ryonlunar/tugas-besar-AAT/services/aggregator/internal/mapper"
)

func TestSubjectForSourceUsesVersionedContract(t *testing.T) {
	tests := []struct {
		source mapper.Source
		want   string
	}{
		{mapper.SourceBMKG, "hazard.events.v1.BMKG"},
		{mapper.SourcePVMBG, "hazard.events.v1.PVMBG"},
	}
	for _, test := range tests {
		t.Run(string(test.source), func(t *testing.T) {
			got, err := SubjectForSource(test.source)
			if err != nil {
				t.Fatalf("SubjectForSource(%s): %v", test.source, err)
			}
			if got != test.want {
				t.Fatalf("SubjectForSource(%s)=%q, want %q", test.source, got, test.want)
			}
		})
	}
	if _, err := SubjectForSource(mapper.Source("OTHER")); err == nil {
		t.Fatal("unknown source must not produce a publish subject")
	}
}
