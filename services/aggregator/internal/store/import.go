package store

import (
	"context"
	"database/sql"
)

func (s *Store) BeginImport(ctx context.Context) (*sql.Tx, error) {
	return s.bmkgWriteDB.BeginTx(ctx, nil)
}
