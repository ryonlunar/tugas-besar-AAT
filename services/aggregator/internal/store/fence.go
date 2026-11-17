package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/ryonlunar/tugas-besar-AAT/contracts"
)

var ErrFenced = errors.New("source ownership has expired")

type fenceKey struct{}
type sourceFence struct {
	source     contracts.Source
	generation int64
	validate   func(context.Context) error
}

func WithFence(ctx context.Context, source contracts.Source, generation int64, validate func(context.Context) error) context.Context {
	return context.WithValue(ctx, fenceKey{}, sourceFence{source, generation, validate})
}

// CheckOwnership gates upstream reads as well as canonical writes. An ordinary
// M1 context has no distributed ownership session and retains its existing flow.
func CheckOwnership(ctx context.Context) error {
	fence, distributed := ctx.Value(fenceKey{}).(sourceFence)
	if !distributed {
		return nil
	}
	if fence.validate == nil || fence.validate(ctx) != nil {
		return ErrFenced
	}
	return nil
}

func (s *Store) RequireFence() { s.requireFence = true }

// InstallFence is a takeover barrier. A previous generation's open transaction
// must finish or expire before this update can return. Generations never regress.
func (s *Store) InstallFence(ctx context.Context, source contracts.Source, generation int64) error {
	if !source.Valid() || generation <= 0 {
		return ErrFenced
	}
	ctx, cancel := s.writeContext(ctx)
	defer cancel()
	var installed int64
	err := s.bmkgWriteDB.QueryRowContext(ctx, `INSERT INTO ingestion_fences(source,generation) VALUES($1,$2)
		ON CONFLICT(source) DO UPDATE SET generation=EXCLUDED.generation
		WHERE ingestion_fences.generation <= EXCLUDED.generation RETURNING generation`, source, generation).Scan(&installed)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrFenced
	}
	return err
}

func checkFence(ctx context.Context, tx *sql.Tx, source contracts.Source) error {
	fence, ok := ctx.Value(fenceKey{}).(sourceFence)
	if !ok || fence.source != source || fence.generation <= 0 || fence.validate == nil {
		return ErrFenced
	}
	// Release a row lock held by a frozen client, including one frozen after a
	// statement completed. The owner must reacquire before it can retry a write.
	if _, err := tx.ExecContext(ctx, `SET LOCAL idle_in_transaction_session_timeout='2s'`); err != nil {
		return err
	}
	var generation int64
	if err := tx.QueryRowContext(ctx, `SELECT generation FROM ingestion_fences WHERE source=$1 FOR SHARE`, source).Scan(&generation); err != nil {
		return err
	}
	if generation != fence.generation {
		return ErrFenced
	}
	if err := fence.validate(ctx); err != nil {
		return ErrFenced
	}
	return nil
}
