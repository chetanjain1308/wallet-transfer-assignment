package service

import (
	"context"

	"github.com/google/uuid"

	"github.com/Robustrade/wallet-transfer-assignment/internal/domain"
)

// Store is the persistence the service needs. Reads that stand alone are served
// directly; anything that mutates goes through Atomic.
type Store interface {
	// Atomic runs fn inside one READ COMMITTED transaction, committing if fn
	// returns nil and rolling back otherwise. READ COMMITTED is required, not
	// incidental: the idempotency claim relies on a statement-level snapshot to
	// read back the row a concurrent duplicate has just committed.
	Atomic(ctx context.Context, fn func(context.Context, Tx) error) error

	CreateWallet(ctx context.Context, w domain.Wallet) error
	WalletByID(ctx context.Context, id string) (domain.Wallet, error)
	TransfersForWallet(ctx context.Context, walletID string, limit int) ([]domain.Transfer, error)
}

// Tx is the set of writes that make up a transfer, bound to a single database
// transaction.
type Tx interface {
	// ClaimIdempotencyKey inserts the key and reports whether this caller won it.
	// A concurrent duplicate blocks here until the first transaction settles.
	ClaimIdempotencyKey(ctx context.Context, key, fingerprint string) (bool, error)
	IdempotencyRecord(ctx context.Context, key string) (IdempotencyRecord, error)
	LinkIdempotencyKey(ctx context.Context, key string, transferID uuid.UUID) error

	// LockWallets takes row locks on both wallets in a fixed order. Both must
	// exist or it returns domain.ErrWalletNotFound.
	LockWallets(ctx context.Context, a, b string) (map[string]domain.Wallet, error)
	AdjustBalance(ctx context.Context, walletID string, delta int64) error

	// CreateTransfer returns the stored transfer, including database-generated
	// timestamps, so the first response matches what a later replay will read back.
	CreateTransfer(ctx context.Context, t domain.Transfer) (domain.Transfer, error)
	// SetTransferStatus applies the transition only if the stored status is still
	// from, so a transfer cannot be settled twice.
	SetTransferStatus(ctx context.Context, t domain.Transfer, from domain.Status) (domain.Transfer, error)
	TransferByID(ctx context.Context, id uuid.UUID) (domain.Transfer, error)

	RecordEntries(ctx context.Context, entries ...domain.LedgerEntry) error
}

// IdempotencyRecord is a claimed key and the transfer it produced.
type IdempotencyRecord struct {
	Key                string
	RequestFingerprint string
	TransferID         uuid.UUID
}
