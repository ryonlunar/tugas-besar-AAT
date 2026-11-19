// Package partition defines stable logical placement, independently of nodes
// and replica addresses. Changing this mapping requires a data migration.
package partition

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/ryonlunar/tugas-besar-AAT/contracts"
)

const BucketCount = 96
const UnitCount = 4

// JournalUnit locates source ownership and intake progress, not canonical events.
func JournalUnit(source contracts.Source) (string, error) {
	switch source {
	case contracts.SourceBMKG:
		return "shard-0", nil
	case contracts.SourcePVMBG:
		return "shard-1", nil
	case contracts.SourceSatellite:
		return "shard-2", nil
	case contracts.SourceCoastal:
		return "shard-3", nil
	default:
		return "", errors.New("valid source required")
	}
}

func Unit(source contracts.Source, refID string) (string, error) {
	if !source.Valid() || refID == "" {
		return "", errors.New("valid source and reference ID required")
	}
	sum := sha256.Sum256([]byte(string(source) + "\x00" + refID))
	bucket := binary.BigEndian.Uint64(sum[:8]) % BucketCount
	return fmt.Sprintf("shard-%d", bucket%UnitCount), nil
}

// ReportUnit places a Field Report by its idempotency scope, so every retry of
// one request reaches the unit holding its receipt.
func ReportUnit(clientID, idempotencyKey string) (string, error) {
	if clientID == "" || idempotencyKey == "" {
		return "", errors.New("client and idempotency key required")
	}
	sum := sha256.Sum256([]byte("field-report\x00" + clientID + "\x00" + idempotencyKey))
	bucket := binary.BigEndian.Uint64(sum[:8]) % BucketCount
	return fmt.Sprintf("shard-%d", bucket%UnitCount), nil
}
