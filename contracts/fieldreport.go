package contracts

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math"
	"regexp"
	"time"
	"unicode/utf8"
)

// MaxFieldReportBytes bounds a Tim Lapangan request body. The schema allows a
// 4096-character description; multi-byte UTF-8 and JSON escaping need headroom.
const MaxFieldReportBytes = 32 << 10

// IdempotencyKeyPattern is the accepted Idempotency-Key header syntax.
var IdempotencyKeyPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

var ErrInvalidFieldReport = errors.New("invalid field report")

// FieldReport mirrors field-report.schema.json.
type FieldReport struct {
	ObservedAt  string  `json:"observed_at"`
	Latitude    float64 `json:"latitude"`
	Longitude   float64 `json:"longitude"`
	Description string  `json:"description"`
}

// ValidateFieldReport applies field-report.schema.json without allocating a
// schema compiler per request. The original bytes stay the stored record.
func ValidateFieldReport(body []byte) (FieldReport, error) {
	var report FieldReport
	if len(body) == 0 || len(body) > MaxFieldReportBytes || !utf8.Valid(body) {
		return report, ErrInvalidFieldReport
	}
	var fields map[string]json.RawMessage
	decoder := json.NewDecoder(bytes.NewReader(body))
	if err := decoder.Decode(&fields); err != nil || fields == nil {
		return report, ErrInvalidFieldReport
	}
	if _, err := decoder.Token(); err != io.EOF {
		return report, ErrInvalidFieldReport
	}
	if len(fields) != 4 {
		return report, ErrInvalidFieldReport
	}
	for _, name := range []string{"observed_at", "latitude", "longitude", "description"} {
		if raw, ok := fields[name]; !ok || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return report, ErrInvalidFieldReport
		}
	}
	strict := json.NewDecoder(bytes.NewReader(body))
	strict.DisallowUnknownFields()
	if err := strict.Decode(&report); err != nil {
		return FieldReport{}, ErrInvalidFieldReport
	}
	if _, err := time.Parse(time.RFC3339Nano, report.ObservedAt); err != nil {
		return FieldReport{}, ErrInvalidFieldReport
	}
	if math.IsNaN(report.Latitude) || report.Latitude < -90 || report.Latitude > 90 ||
		math.IsNaN(report.Longitude) || report.Longitude < -180 || report.Longitude > 180 {
		return FieldReport{}, ErrInvalidFieldReport
	}
	if n := utf8.RuneCountInString(report.Description); n < 1 || n > 4096 {
		return FieldReport{}, ErrInvalidFieldReport
	}
	return report, nil
}
