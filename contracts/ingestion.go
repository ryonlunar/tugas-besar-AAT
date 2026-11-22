package contracts

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"time"
)

// RawKey preserves successive versions when an upstream reuses an identifier.
// The source journal namespaces this key by source. Exact retransmissions dedupe;
// changed bytes remain inspectable instead of conflicting with an earlier record.
func RawKey(upstreamID string, payload []byte) string {
	hash := sha256.New()
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], uint64(len(upstreamID)))
	hash.Write(size[:])
	hash.Write([]byte(upstreamID))
	hash.Write(payload)
	return hex.EncodeToString(hash.Sum(nil))
}

// RawRecord preserves source bytes before decoding. Key must uniquely identify an
// immutable source message (for example, a satellite offset, not a detection ID).
// []byte deliberately preserves malformed JSON and invalid UTF-8.
type RawRecord struct {
	Key           string
	Kind          string
	Payload       []byte
	ReceivedAt    time.Time
	CorrelationID string
	OrderingKey   string // Stable entity identity; a failed version blocks later versions of this entity.
	Auxiliary     []byte // Related input needed by the mapper (for example, tsunami warnings).
	Snapshot      bool   // Polling snapshots suppress unchanged consecutive entity versions only.
}

// Batch commits records and their resume position together. From is the position
// used to obtain this batch; Cursor is the next position to read.
type Batch struct {
	From           string
	Cursor         string
	Records        []RawRecord
	DegradedReason string // Optional adapter-reported health failure; raw records still commit.
}

// SourceAdapter resumes from the supplied opaque cursor. It must stop on a
// commit error and must not advance its local cursor before commit succeeds.
// Polling adapters keep their polling loop inside Run so ownership survives
// between polls. A finite fixture may return at EOF.
type SourceAdapter interface {
	Run(context.Context, string, func(context.Context, Batch) error) error
}

type MutationKind string

const (
	MutationUpsert  MutationKind = "upsert"
	MutationRetract MutationKind = "retract"
)

// Mutation is a processor's output, not a JetStream envelope. Storage assigns
// hazard_id, revision, and ingested_at. Retraction identifies an existing event
// by Source/SourceRefID and leaves Event nil; an upsert supplies its canonical data.
// An upsert's Event.Source and Event.SourceRefID must match its outer identity.
type Mutation struct {
	Kind        MutationKind
	Source      Source
	SourceRefID string
	Event       *HazardEvent
}
