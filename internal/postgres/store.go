// Package postgres implements the service's Store against PostgreSQL.
package postgres

import (
	"context"
	"database/sql"
	"fmt"

	_ "github.com/jackc/pgx/v5/stdlib" // database/sql driver

	"github.com/Robustrade/wallet-transfer-assignment/internal/service"
	"github.com/Robustrade/wallet-transfer-assignment/migrations"
)

// Store is the PostgreSQL-backed service.Store.
type Store struct {
	db *sql.DB
}

// Open connects to dsn and verifies the connection.
func Open(ctx context.Context, dsn string) (*Store, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}
	return &Store{db: db}, nil
}

// Close releases the connection pool.
func (s *Store) Close() error {
	return s.db.Close()
}

// migrationLockKey namespaces the advisory lock Migrate takes. Any constant
// works as long as every instance uses the same one.
const migrationLockKey = 8675309

// Migrate applies the schema. Every statement is idempotent, so this is safe to
// run on every start.
//
// The advisory lock serialises instances starting at the same time. Without it
// two concurrent CREATE TYPE statements collide on a unique index in the catalog,
// which the DO block's duplicate_object handler does not catch.
func (s *Store) Migrate(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin migration: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, migrationLockKey); err != nil {
		return fmt.Errorf("take migration lock: %w", err)
	}
	if _, err := tx.ExecContext(ctx, migrations.Schema); err != nil {
		return fmt.Errorf("apply schema: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migration: %w", err)
	}
	return nil
}

// Atomic runs fn in a READ COMMITTED transaction.
//
// The isolation level is deliberate. ClaimIdempotencyKey depends on a losing
// INSERT ... ON CONFLICT DO NOTHING being able to read back the row the winner
// just committed; under REPEATABLE READ that same statement raises a
// serialization failure instead, and the duplicate would have to be retried by
// the caller to get the same answer.
func (s *Store) Atomic(ctx context.Context, fn func(context.Context, service.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	// Unconditional, so a panic inside fn cannot leave the transaction open and
	// its pool connection pinned. Rollback after a successful commit is a no-op.
	defer func() { _ = tx.Rollback() }()

	if err := fn(ctx, &Tx{tx: tx}); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}
	return nil
}

// Tx implements service.Tx against one database transaction.
type Tx struct {
	tx *sql.Tx
}
