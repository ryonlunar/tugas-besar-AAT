package bmkgimport

import (
	"testing"
	"time"

	"github.com/ryonlunar/tugas-besar-AAT/services/aggregator/internal/mapper"
)

const testHeader = "event_id,occurred_at,latitude,longitude,depth_km,magnitude,region_name,potential_tsunami"

func TestToSeismicJSONMapsThroughExistingMapper(t *testing.T) {
	row := "synthetic-eq-00001,2023-11-14T22:15:37Z,-0.7313,100.2369,10.6,6.4,Sumatera Barat,false"
	raw, err := ToSeismicJSON([]byte(testHeader), []byte(row))
	if err != nil {
		t.Fatalf("ToSeismicJSON: %v", err)
	}

	m := mapper.New(mapper.DefaultVolcanoLocations())
	event, err := m.MapSeismic(raw, nil, "00000000-0000-4000-8000-000000000001", time.Now())
	if err != nil {
		t.Fatalf("MapSeismic rejected ToSeismicJSON's output: %v\nraw=%s", err, raw)
	}
	if event.SourceRefID != "synthetic-eq-00001" {
		t.Fatalf("SourceRefID = %q, want event_id synthetic-eq-00001", event.SourceRefID)
	}
	if event.Attributes["magnitude"] != 6.4 {
		t.Errorf("magnitude = %v, want 6.4", event.Attributes["magnitude"])
	}
	if event.Attributes["depth_km"] != 10.6 {
		t.Errorf("depth_km = %v, want 10.6", event.Attributes["depth_km"])
	}
	if event.Attributes["potential_tsunami"] != false {
		t.Errorf("potential_tsunami = %v, want false", event.Attributes["potential_tsunami"])
	}
}

func TestToSeismicJSONRejectsMismatchedColumnCount(t *testing.T) {
	_, err := ToSeismicJSON([]byte(testHeader), []byte("only,three,fields"))
	if err == nil {
		t.Fatal("a row with fewer fields than the header must be rejected")
	}
}

func TestToSeismicJSONRejectsUnknownColumn(t *testing.T) {
	header := testHeader + ",unexpected_extra_column"
	row := "e1,2023-11-14T22:15:37Z,-0.73,100.23,10.6,6.4,Sumatera Barat,false,surprise"
	_, err := ToSeismicJSON([]byte(header), []byte(row))
	if err == nil {
		t.Fatal("an unrecognized column must be rejected, not silently dropped")
	}
}

func TestToSeismicJSONRejectsMissingRequiredColumn(t *testing.T) {
	headerMissingMagnitude := "event_id,occurred_at,latitude,longitude,depth_km,region_name,potential_tsunami"
	row := "e1,2023-11-14T22:15:37Z,-0.73,100.23,10.6,Sumatera Barat,false"
	_, err := ToSeismicJSON([]byte(headerMissingMagnitude), []byte(row))
	if err == nil {
		t.Fatal("a header missing a required mapper field must be rejected")
	}
}

func TestToSeismicJSONRejectsNonNumericMagnitude(t *testing.T) {
	row := "e1,2023-11-14T22:15:37Z,-0.73,100.23,10.6,not-a-number,Sumatera Barat,false"
	_, err := ToSeismicJSON([]byte(testHeader), []byte(row))
	if err == nil {
		t.Fatal("a non-numeric magnitude must be rejected before reaching the mapper")
	}
}

func TestFieldValueExtractsEventID(t *testing.T) {
	row := "synthetic-eq-00042,2023-11-14T22:15:37Z,-0.73,100.23,10.6,6.4,Sumatera Barat,false"
	got, err := FieldValue([]byte(testHeader), []byte(row), "event_id")
	if err != nil {
		t.Fatalf("FieldValue: %v", err)
	}
	if got != "synthetic-eq-00042" {
		t.Fatalf("FieldValue(event_id) = %q, want synthetic-eq-00042", got)
	}
}
