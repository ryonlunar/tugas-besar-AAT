// Package audit defines the assessed audit responses and the owner's internal
// failed-input API. Client-facing authorization remains in Client API.
package audit

import (
	"context"
	"errors"
	"github.com/ryonlunar/tugas-besar-AAT/contracts"
	"time"
)

var ErrQuery = errors.New("invalid audit query or cursor")

type Query struct {
	Source contracts.Source
	Since  time.Time
	Limit  int
	Cursor string
}
type Page[T any] struct {
	Items            []T      `json:"items"`
	NextCursor       *string  `json:"next_cursor"`
	Partial          bool     `json:"partial"`
	UnavailableUnits []string `json:"unavailable_units"`
}
type Checksum struct {
	Source           contracts.Source `json:"source"`
	Count            int              `json:"count"`
	Checksum         string           `json:"checksum"`
	Algorithm        string           `json:"algorithm"`
	AsOf             string           `json:"as_of"`
	Partial          bool             `json:"partial"`
	UnavailableUnits []string         `json:"unavailable_units"`
}
type Location struct {
	Node string `json:"node"`
	Unit string `json:"unit"`
	Role string `json:"role"`
}
type Locations struct {
	HazardID         string     `json:"hazard_id"`
	Locations        []Location `json:"locations"`
	Partial          bool       `json:"partial"`
	UnavailableUnits []string   `json:"unavailable_units"`
}
type Failed struct {
	MessageID  string           `json:"message_id"`
	Source     contracts.Source `json:"source"`
	ReceivedAt string           `json:"received_at"`
	Reason     string           `json:"reason"`
	Retryable  bool             `json:"retryable"`
	Attempts   int              `json:"attempts"`
	Status     string           `json:"status"`
	Sequence   int64            `json:"-"`
}
type Detail struct {
	Failed
	Kind          string `json:"kind"`
	OrderingKey   string `json:"ordering_key"`
	CorrelationID string `json:"correlation_id"`
	Payload       []byte `json:"payload_base64"`
	Auxiliary     []byte `json:"auxiliary_base64"`
}
type Replay struct {
	MessageID string `json:"message_id"`
	Status    string `json:"status"`
}
type Repository interface {
	AuditEvents(context.Context, Query) (Page[contracts.HazardEvent], error)
	AuditChecksum(context.Context, contracts.Source) (Checksum, error)
	AuditLocation(context.Context, string) (Locations, bool, error)
	AuditUnprocessed(context.Context, Query) (Page[Failed], error)
	InspectFailed(context.Context, string) (Detail, bool, error)
	ReplayFailed(context.Context, string) (Replay, bool, error)
}
