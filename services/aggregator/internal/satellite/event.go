package satellite

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"time"
)

// Detection is a full detection observation decoded from a `detection`
// event's nested `detection` object. The fields fire recognition depends on
// (acq_time, frp, latitude, longitude) are preserved as their original JSON
// lexical token via json.Decoder.UseNumber, never round-tripped through
// float64: a float64 reformat can change or round the printed digits (e.g.
// "1.33702" -> "1.5123"-style rewrites), which would break the detection_key
// and audited attributes (A.events.5). Lat/Lon additionally carry a parsed
// float64 for geometry (Haversine, clustering) -- FRP has no float64
// counterpart because its aggregation (D8) sums json.Number tokens directly
// via big.Rat, never through float64.
type Detection struct {
	Satellite   string
	AcqDate     string
	AcqTimeText string
	Confidence  string
	FRPText     string
	LatText     string
	Lat         float64
	LonText     string
	Lon         float64
	// Extra holds every `detection` field not listed above, verbatim and
	// unvalidated (version, scan, bright_ti4/5, daynight, instrument,
	// track, and any future additive field). Fire recognition never reads
	// it; it exists for reporting only.
	Extra map[string]json.RawMessage
	// At is the UTC detection time computed by DetectedAt(AcqDate,
	// AcqTimeText) (FIRE.2). ParseFeedEvent leaves it zero: deriving it is
	// a separate, independently testable step (detection.go), and a
	// malformed acq_date/acq_time should reject the detection with its
	// own reason rather than fail silently inside event decoding.
	At time.Time
}

// Update is a partial correction referencing an existing detection by its
// detection_key. Changes holds exactly the fields the feed reported as
// changed, keyed by their JSON field name, with each value preserved as the
// exact source bytes (json.RawMessage decodes verbatim regardless of
// UseNumber, so this needs no extra lexical-preservation step). Merging
// Changes onto previously stored state is the recognizer's job (Fase 4), not
// this parser's -- ParseFeedEvent only decodes the wire shape.
type Update struct {
	Key     string
	Changes map[string]json.RawMessage
}

// Retract withdraws a previously seen detection by its detection_key. Reason
// is free text for audit/log purposes only (observed value:
// "false_positive"); it never changes control flow, and the feed is not
// guaranteed to always send it, so it defaults to "" when absent.
type Retract struct {
	Key    string
	Reason string
}

// RejectError marks a malformed feed event -- a required field missing, a
// known field with the wrong JSON type, an SSE id that disagrees with the
// body's own offset field, or an event type ParseFeedEvent does not
// recognize. ParseFeedEvent never panics; every failure is a *RejectError.
type RejectError struct{ Reason string }

func (e *RejectError) Error() string { return e.Reason }

// rawEnvelope captures the feed's common wrapper shape. `data` on the wire
// is this wrapper object, not a detection document directly: every event
// kind carries "offset", and detection/update/retract each add their own
// fields alongside it.
type rawEnvelope struct {
	Offset    json.RawMessage `json:"offset"`
	Detection json.RawMessage `json:"detection"`
	Key       json.RawMessage `json:"key"`
	Changes   json.RawMessage `json:"changes"`
	Reason    json.RawMessage `json:"reason"`
}

// ParseFeedEvent decodes one SSE Event's Data as a satellite feed envelope.
// It cross-validates the SSE id against the body's own "offset" field
// (neither is trusted alone) and, when that holds, returns the offset plus
// one of Detection, Update, or Retract depending on ev.Type -- three
// distinct shapes, not one Detection forced to carry update/retract too.
// Any failure -- malformed JSON, a missing/mistyped field, an id/offset
// mismatch, or an unrecognized ev.Type -- is returned as a *RejectError,
// never a panic, so a single corrupt event never aborts a whole batch.
func ParseFeedEvent(ev Event) (offset int64, payload any, err error) {
	sseOffset, idErr := parseOffsetToken(ev.ID)
	if idErr != nil {
		return 0, nil, &RejectError{Reason: fmt.Sprintf("SSE id %q tidak valid: %s", ev.ID, idErr)}
	}
	var env rawEnvelope
	if err := json.Unmarshal(ev.Data, &env); err != nil {
		return 0, nil, &RejectError{Reason: "data event bukan objek JSON valid"}
	}
	bodyOffsetNumber, err := requiredNumber(env.Offset, "offset")
	if err != nil {
		return 0, nil, err
	}
	bodyOffset, convErr := strconv.ParseInt(string(bodyOffsetNumber), 10, 64)
	if convErr != nil {
		return 0, nil, &RejectError{Reason: fmt.Sprintf("field 'offset' %q bukan bilangan bulat", string(bodyOffsetNumber))}
	}
	if bodyOffset != sseOffset {
		return 0, nil, &RejectError{Reason: fmt.Sprintf("id SSE %d tidak sama dengan field offset %d", sseOffset, bodyOffset)}
	}

	switch ev.Type {
	case "detection":
		detection, err := decodeDetection(env.Detection)
		if err != nil {
			return 0, nil, err
		}
		return sseOffset, detection, nil
	case "update":
		key, err := requiredString(env.Key, "key")
		if err != nil {
			return 0, nil, err
		}
		changesRaw, err := requiredObject(env.Changes, "changes")
		if err != nil {
			return 0, nil, err
		}
		var changes map[string]json.RawMessage
		if err := json.Unmarshal(changesRaw, &changes); err != nil {
			return 0, nil, &RejectError{Reason: "field 'changes' bukan objek JSON valid"}
		}
		return sseOffset, Update{Key: key, Changes: changes}, nil
	case "retract":
		key, err := requiredString(env.Key, "key")
		if err != nil {
			return 0, nil, err
		}
		reason, err := optionalString(env.Reason, "reason")
		if err != nil {
			return 0, nil, err
		}
		return sseOffset, Retract{Key: key, Reason: reason}, nil
	default:
		return 0, nil, &RejectError{Reason: fmt.Sprintf("jenis event %q tidak dikenal", ev.Type)}
	}
}

// knownDetectionFields lists the `detection` object keys fire recognition
// consumes; every other key is archived verbatim in Detection.Extra.
var knownDetectionFields = map[string]bool{
	"satellite": true, "acq_date": true, "acq_time": true,
	"confidence": true, "frp": true, "latitude": true, "longitude": true,
}

func decodeDetection(raw json.RawMessage) (Detection, error) {
	object, err := requiredObject(raw, "detection")
	if err != nil {
		return Detection{}, err
	}

	// Known numeric/lexical fields decode through json.Decoder.UseNumber so
	// the original digit sequence (e.g. "1.33702") survives untouched; a
	// float64 round-trip would reformat or round it and break A.events.5.
	decoder := json.NewDecoder(bytes.NewReader(object))
	decoder.UseNumber()
	var fields map[string]any
	if err := decoder.Decode(&fields); err != nil {
		return Detection{}, &RejectError{Reason: "field 'detection' bukan objek JSON valid"}
	}

	satellite, err := requiredStringAny(fields, "satellite")
	if err != nil {
		return Detection{}, err
	}
	acqDate, err := requiredStringAny(fields, "acq_date")
	if err != nil {
		return Detection{}, err
	}
	acqTime, err := requiredNumberAny(fields, "acq_time")
	if err != nil {
		return Detection{}, err
	}
	confidence, err := requiredStringAny(fields, "confidence")
	if err != nil {
		return Detection{}, err
	}
	frp, err := requiredNumberAny(fields, "frp")
	if err != nil {
		return Detection{}, err
	}
	latNumber, err := requiredNumberAny(fields, "latitude")
	if err != nil {
		return Detection{}, err
	}
	lat, convErr := latNumber.Float64()
	if convErr != nil {
		return Detection{}, &RejectError{Reason: "field 'latitude' bukan angka yang valid"}
	}
	lonNumber, err := requiredNumberAny(fields, "longitude")
	if err != nil {
		return Detection{}, err
	}
	lon, convErr := lonNumber.Float64()
	if convErr != nil {
		return Detection{}, &RejectError{Reason: "field 'longitude' bukan angka yang valid"}
	}

	// Passthrough fields are archived as exact bytes (not re-encoded via the
	// UseNumber/any decode above, which would be safe for A.events.5 but is
	// unnecessary complexity for fields recognition never reads).
	var allFields map[string]json.RawMessage
	if err := json.Unmarshal(object, &allFields); err != nil {
		return Detection{}, &RejectError{Reason: "field 'detection' bukan objek JSON valid"}
	}
	extra := make(map[string]json.RawMessage, len(allFields))
	for key, value := range allFields {
		if knownDetectionFields[key] {
			continue
		}
		extra[key] = value
	}

	return Detection{
		Satellite:   satellite,
		AcqDate:     acqDate,
		AcqTimeText: string(acqTime),
		Confidence:  confidence,
		FRPText:     string(frp),
		LatText:     string(latNumber),
		Lat:         lat,
		LonText:     string(lonNumber),
		Lon:         lon,
		Extra:       extra,
	}, nil
}

// parseOffsetToken requires id to be a non-negative, all-digit base-10
// integer -- the shape observed for SSE id and the feed's offset field.
func parseOffsetToken(id string) (int64, error) {
	if id == "" {
		return 0, fmt.Errorf("kosong")
	}
	for _, r := range id {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("bukan bilangan bulat non-negatif")
		}
	}
	return strconv.ParseInt(id, 10, 64)
}

// jsonKind classifies an already-syntactically-valid JSON value's leading
// token for error messages, without allocating a decode target. An empty
// (missing) raw message is reported distinctly from JSON null.
func jsonKind(raw json.RawMessage) string {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return "missing"
	}
	switch trimmed[0] {
	case '"':
		return "string"
	case '{':
		return "object"
	case '[':
		return "array"
	case 't', 'f':
		return "boolean"
	case 'n':
		return "null"
	default:
		return "number"
	}
}

func requiredString(raw json.RawMessage, field string) (string, error) {
	kind := jsonKind(raw)
	if kind == "missing" {
		return "", &RejectError{Reason: fmt.Sprintf("field wajib '%s' hilang", field)}
	}
	if kind != "string" {
		return "", &RejectError{Reason: fmt.Sprintf("field '%s' berubah dari %s menjadi string", field, kind)}
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", &RejectError{Reason: fmt.Sprintf("field '%s' bukan string JSON valid", field)}
	}
	return value, nil
}

func optionalString(raw json.RawMessage, field string) (string, error) {
	if jsonKind(raw) == "missing" {
		return "", nil
	}
	return requiredString(raw, field)
}

func requiredNumber(raw json.RawMessage, field string) (json.Number, error) {
	kind := jsonKind(raw)
	if kind == "missing" {
		return "", &RejectError{Reason: fmt.Sprintf("field wajib '%s' hilang", field)}
	}
	if kind != "number" {
		return "", &RejectError{Reason: fmt.Sprintf("field '%s' berubah dari %s menjadi number", field, kind)}
	}
	return json.Number(bytes.TrimSpace(raw)), nil
}

func requiredObject(raw json.RawMessage, field string) (json.RawMessage, error) {
	kind := jsonKind(raw)
	if kind == "missing" {
		return nil, &RejectError{Reason: fmt.Sprintf("field wajib '%s' hilang", field)}
	}
	if kind != "object" {
		return nil, &RejectError{Reason: fmt.Sprintf("field '%s' berubah dari %s menjadi object", field, kind)}
	}
	return raw, nil
}

// anyKind classifies a value produced by decoding with json.Decoder.UseNumber
// (so numbers surface as json.Number rather than float64).
func anyKind(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case string:
		return "string"
	case json.Number:
		return "number"
	case bool:
		return "boolean"
	case map[string]any:
		return "object"
	case []any:
		return "array"
	default:
		return "unknown"
	}
}

func requiredStringAny(fields map[string]any, field string) (string, error) {
	v, ok := fields[field]
	if !ok {
		return "", &RejectError{Reason: fmt.Sprintf("field wajib '%s' hilang", field)}
	}
	s, ok := v.(string)
	if !ok {
		return "", &RejectError{Reason: fmt.Sprintf("field '%s' berubah dari %s menjadi string", field, anyKind(v))}
	}
	return s, nil
}

func requiredNumberAny(fields map[string]any, field string) (json.Number, error) {
	v, ok := fields[field]
	if !ok {
		return "", &RejectError{Reason: fmt.Sprintf("field wajib '%s' hilang", field)}
	}
	n, ok := v.(json.Number)
	if !ok {
		return "", &RejectError{Reason: fmt.Sprintf("field '%s' berubah dari %s menjadi number", field, anyKind(v))}
	}
	return n, nil
}
