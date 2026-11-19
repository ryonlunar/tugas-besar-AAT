// Package bmkgimport loads a BMKG historical-catalog CSV file once, as a
// phase before BMKG's live polling takes over (D11). The official dataset's
// format is not yet known; everything here is built and tested against a
// SYNTHETIC fixture (testdata/synthetic-catalog.csv) and is designed so
// that adapting to the real format is a change to ToSeismicJSON's column
// map alone, not to the adapter, cursor, or wiring logic.
package bmkgimport

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"strings"
)

// seismicField maps a recognized CSV column name to the exact JSON field
// name mapper.MapSeismic requires (services/aggregator/internal/mapper).
// A column not listed here makes ToSeismicJSON fail rather than silently
// drop it -- an unrecognized column in a historical catalog is far more
// likely a mistaken assumption about the format than noise to ignore.
var seismicField = map[string]string{
	"event_id":          "event_id",
	"occurred_at":       "occurred_at",
	"latitude":          "epicenter_lat",
	"longitude":         "epicenter_lon",
	"depth_km":          "depth_km",
	"magnitude":         "magnitude",
	"region_name":       "region_name",
	"potential_tsunami": "potential_tsunami",
}

// numericColumns are copied into the output JSON as a bare number token,
// taken verbatim from the row's own text -- never parsed through float64
// and reformatted, so the historical catalog's own numeric precision
// survives untouched into the mapper and, eventually, the audited
// HazardEvent.
var numericColumns = map[string]bool{"latitude": true, "longitude": true, "depth_km": true, "magnitude": true}

var boolColumns = map[string]bool{"potential_tsunami": true}

// requiredFields are every mapper.MapSeismic field ToSeismicJSON must
// produce; any missing after parsing the header is an error, not a
// zero-filled default (a quietly-wrong magnitude or coordinate is worse
// than a parked import).
var requiredFields = []string{"event_id", "occurred_at", "epicenter_lat", "epicenter_lon", "depth_km", "magnitude", "region_name", "potential_tsunami"}

// ToSeismicJSON converts one CSV data row, given its header line, into the
// exact JSON object shape mapper.MapSeismic consumes for a "seismic_event"
// input. Both header and row are the raw bytes as they appeared in the
// catalog file (no trimming beyond what encoding/csv itself does).
func ToSeismicJSON(header, row []byte) ([]byte, error) {
	columns, err := parseCSVLine(header)
	if err != nil {
		return nil, fmt.Errorf("bmkgimport: header: %w", err)
	}
	values, err := parseCSVLine(row)
	if err != nil {
		return nil, fmt.Errorf("bmkgimport: row: %w", err)
	}
	if len(values) != len(columns) {
		return nil, fmt.Errorf("bmkgimport: row has %d fields, header has %d", len(values), len(columns))
	}

	var buf bytes.Buffer
	buf.WriteByte('{')
	seen := make(map[string]bool, len(columns))
	wrote := false
	for i, col := range columns {
		field, known := seismicField[col]
		if !known {
			return nil, fmt.Errorf("bmkgimport: unrecognized column %q", col)
		}
		if seen[field] {
			return nil, fmt.Errorf("bmkgimport: duplicate column %q", col)
		}
		seen[field] = true
		value := values[i]
		if wrote {
			buf.WriteByte(',')
		}
		wrote = true
		fieldName, _ := json.Marshal(field)
		buf.Write(fieldName)
		buf.WriteByte(':')
		switch {
		case numericColumns[col]:
			if !isJSONNumberToken(value) {
				return nil, fmt.Errorf("bmkgimport: column %q value %q is not a valid number", col, value)
			}
			buf.WriteString(value)
		case boolColumns[col]:
			if value != "true" && value != "false" {
				return nil, fmt.Errorf("bmkgimport: column %q value %q must be true or false", col, value)
			}
			buf.WriteString(value)
		default:
			encoded, err := json.Marshal(value)
			if err != nil {
				return nil, fmt.Errorf("bmkgimport: encode column %q: %w", col, err)
			}
			buf.Write(encoded)
		}
	}
	for _, field := range requiredFields {
		if !seen[field] {
			return nil, fmt.Errorf("bmkgimport: catalog header is missing a column for required field %q", field)
		}
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

// FieldValue extracts one column's raw text value from a row, given the
// catalog's header line -- used by the adapter to pull event_id out
// without building the full JSON object for every row.
func FieldValue(header, row []byte, column string) (string, error) {
	columns, err := parseCSVLine(header)
	if err != nil {
		return "", fmt.Errorf("bmkgimport: header: %w", err)
	}
	values, err := parseCSVLine(row)
	if err != nil {
		return "", fmt.Errorf("bmkgimport: row: %w", err)
	}
	if len(values) != len(columns) {
		return "", fmt.Errorf("bmkgimport: row has %d fields, header has %d", len(values), len(columns))
	}
	for i, col := range columns {
		if col == column {
			return values[i], nil
		}
	}
	return "", fmt.Errorf("bmkgimport: column %q not present in header", column)
}

func parseCSVLine(line []byte) ([]string, error) {
	r := csv.NewReader(bytes.NewReader(line))
	record, err := r.Read()
	if err != nil {
		return nil, err
	}
	return record, nil
}

// isJSONNumberToken reports whether value is a syntactically valid JSON
// number token (optional '-', digits, optional fractional part, optional
// exponent) -- the same shape json.Marshal would ever produce for a
// float64, checked here without round-tripping through float64 at all.
func isJSONNumberToken(value string) bool {
	if value == "" {
		return false
	}
	i := 0
	if value[i] == '-' {
		i++
	}
	if i >= len(value) {
		return false
	}
	start := i
	for i < len(value) && value[i] >= '0' && value[i] <= '9' {
		i++
	}
	if i == start {
		return false
	}
	if i < len(value) && value[i] == '.' {
		i++
		fracStart := i
		for i < len(value) && value[i] >= '0' && value[i] <= '9' {
			i++
		}
		if i == fracStart {
			return false
		}
	}
	if i < len(value) && (value[i] == 'e' || value[i] == 'E') {
		i++
		if i < len(value) && (value[i] == '+' || value[i] == '-') {
			i++
		}
		expStart := i
		for i < len(value) && value[i] >= '0' && value[i] <= '9' {
			i++
		}
		if i == expStart {
			return false
		}
	}
	return i == len(value) && !strings.ContainsAny(value, " \t")
}
