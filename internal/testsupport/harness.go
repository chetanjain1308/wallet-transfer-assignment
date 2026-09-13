// Package testsupport wires tests to a real PostgreSQL instance.
//
// The concurrency and idempotency guarantees under test are properties of
// Postgres row locks and unique indexes, so there is no in-memory substitute
// that would prove anything: an in-memory fake would pass while the real system
// double-spent.
package testsupport

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/Robustrade/wallet-transfer-assignment/internal/config"
	"github.com/Robustrade/wallet-transfer-assignment/internal/domain"
	"github.com/Robustrade/wallet-transfer-assignment/internal/postgres"
)

// Harness is a connected, migrated database plus the helpers tests assert with.
type Harness struct {
	Store *postgres.Store
	DB    *sql.DB

	t *testing.T
}

// migrateOnce keeps the schema application to one per test binary; every test
// wants the same schema and re-running the DDL per test proves nothing.
var migrateOnce sync.Once

// New connects to the test database and applies the schema.
//
// Tests name their own wallets with unique ids rather than truncating tables, so
// packages and cases can run in parallel against one database.
func New(t *testing.T) *Harness {
	t.Helper()

	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		dsn = config.DefaultDatabaseURL
	}

	ctx := context.Background()
	store, err := postgres.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("connect to the test database: %v\n\nStart it with: docker compose up -d", err)
	}

	var migrateErr error
	migrateOnce.Do(func() { migrateErr = store.Migrate(ctx) })
	if migrateErr != nil {
		t.Fatalf("apply schema: %v", migrateErr)
	}

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open assertion connection: %v", err)
	}

	t.Cleanup(func() {
		_ = db.Close()
		_ = store.Close()
	})

	return &Harness{Store: store, DB: db, t: t}
}

// Logger returns a logger that discards output.
func Logger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// Wallet creates a wallet with a unique id and returns it.
func (h *Harness) Wallet(currency string, balance int64) string {
	h.t.Helper()

	id := fmt.Sprintf("wallet_%s", uuid.NewString())
	err := h.Store.CreateWallet(context.Background(), domain.Wallet{
		ID:       id,
		Currency: currency,
		Balance:  balance,
	})
	if err != nil {
		h.t.Fatalf("create wallet: %v", err)
	}
	return id
}

// Balance reads a wallet's stored balance.
func (h *Harness) Balance(walletID string) int64 {
	h.t.Helper()

	wallet, err := h.Store.WalletByID(context.Background(), walletID)
	if err != nil {
		h.t.Fatalf("read balance of %q: %v", walletID, err)
	}
	return wallet.Balance
}

// LedgerBalance sums the wallet's ledger entries. It must always equal the
// stored balance minus whatever the wallet opened with.
func (h *Harness) LedgerBalance(walletID string) int64 {
	h.t.Helper()

	const q = `
		SELECT COALESCE(SUM(CASE WHEN type = 'DEBIT' THEN -amount ELSE amount END), 0)
		FROM ledger_entries
		WHERE wallet_id = $1`

	var sum int64
	if err := h.DB.QueryRow(q, walletID).Scan(&sum); err != nil {
		h.t.Fatalf("sum ledger for %q: %v", walletID, err)
	}
	return sum
}

// EntryCount counts the ledger entries written for a transfer.
func (h *Harness) EntryCount(transferID uuid.UUID) int {
	h.t.Helper()

	const q = `SELECT count(*) FROM ledger_entries WHERE transfer_id = $1`

	var count int
	if err := h.DB.QueryRow(q, transferID).Scan(&count); err != nil {
		h.t.Fatalf("count entries for %s: %v", transferID, err)
	}
	return count
}

// TransferCountForKey counts transfers stored against an idempotency key. It is
// the direct check that a duplicate request created nothing.
func (h *Harness) TransferCountForKey(key string) int {
	h.t.Helper()

	const q = `SELECT count(*) FROM transfers WHERE idempotency_key = $1`

	var count int
	if err := h.DB.QueryRow(q, key).Scan(&count); err != nil {
		h.t.Fatalf("count transfers for key %q: %v", key, err)
	}
	return count
}

// Key returns an idempotency key unique to this test run.
func Key() string {
	return uuid.NewString()
}
