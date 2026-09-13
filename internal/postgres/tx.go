package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/Robustrade/wallet-transfer-assignment/internal/domain"
	"github.com/Robustrade/wallet-transfer-assignment/internal/service"
)

// ClaimIdempotencyKey inserts the key and reports whether this transaction won
// it. ON CONFLICT DO NOTHING rather than a SELECT-then-INSERT: the primary key
// is what serialises concurrent duplicates, and a losing caller blocks here
// until the winner commits or rolls back.
func (t *Tx) ClaimIdempotencyKey(ctx context.Context, key, fingerprint string) (bool, error) {
	const q = `
		INSERT INTO idempotency_records (key, request_fingerprint)
		VALUES ($1, $2)
		ON CONFLICT (key) DO NOTHING`

	result, err := t.tx.ExecContext(ctx, q, key, fingerprint)
	if err != nil {
		return false, fmt.Errorf("claim idempotency key: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("claim idempotency key: %w", err)
	}
	return rows == 1, nil
}

// IdempotencyRecord reads a claimed key.
func (t *Tx) IdempotencyRecord(ctx context.Context, key string) (service.IdempotencyRecord, error) {
	const q = `
		SELECT key, request_fingerprint, transfer_id
		FROM idempotency_records
		WHERE key = $1`

	var (
		record     service.IdempotencyRecord
		transferID uuid.NullUUID
	)
	err := t.tx.QueryRowContext(ctx, q, key).
		Scan(&record.Key, &record.RequestFingerprint, &transferID)
	if err != nil {
		return service.IdempotencyRecord{}, fmt.Errorf("read idempotency record %q: %w", key, err)
	}
	record.TransferID = transferID.UUID
	return record, nil
}

// LinkIdempotencyKey points the claimed key at the transfer it produced.
func (t *Tx) LinkIdempotencyKey(ctx context.Context, key string, transferID uuid.UUID) error {
	const q = `UPDATE idempotency_records SET transfer_id = $2 WHERE key = $1`

	if _, err := t.tx.ExecContext(ctx, q, key, transferID); err != nil {
		return fmt.Errorf("link idempotency key: %w", err)
	}
	return nil
}

// LockWallets takes row locks on both wallets.
//
// ORDER BY id is the deadlock guard: concurrent opposing transfers (A to B and B
// to A) would otherwise take the same two locks in opposite orders and one would
// be aborted by the deadlock detector.
func (t *Tx) LockWallets(ctx context.Context, a, b string) (map[string]domain.Wallet, error) {
	const q = `
		SELECT id, currency, balance, created_at, updated_at
		FROM wallets
		WHERE id IN ($1, $2)
		ORDER BY id
		FOR UPDATE`

	rows, err := t.tx.QueryContext(ctx, q, a, b)
	if err != nil {
		return nil, fmt.Errorf("lock wallets: %w", err)
	}
	defer rows.Close()

	wallets := make(map[string]domain.Wallet, 2)
	for rows.Next() {
		var w domain.Wallet
		if err := rows.Scan(&w.ID, &w.Currency, &w.Balance, &w.CreatedAt, &w.UpdatedAt); err != nil {
			return nil, fmt.Errorf("lock wallets: %w", err)
		}
		wallets[w.ID] = w
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("lock wallets: %w", err)
	}
	if len(wallets) != 2 {
		return nil, domain.ErrWalletNotFound
	}
	return wallets, nil
}

// AdjustBalance applies delta to a wallet already locked by this transaction.
// The non-negative balance constraint is the database's own backstop against a
// debit slipping past the service's check.
func (t *Tx) AdjustBalance(ctx context.Context, walletID string, delta int64) error {
	const q = `
		UPDATE wallets
		SET balance = balance + $2, updated_at = now()
		WHERE id = $1`

	if _, err := t.tx.ExecContext(ctx, q, walletID, delta); err != nil {
		return fmt.Errorf("adjust balance of %q: %w", walletID, err)
	}
	return nil
}

// CreateTransfer writes the transfer in its initial state and returns it with
// the timestamps the database generated.
func (t *Tx) CreateTransfer(ctx context.Context, transfer domain.Transfer) (domain.Transfer, error) {
	const q = `
		INSERT INTO transfers (
			id, idempotency_key, from_wallet_id, to_wallet_id,
			amount, currency, status
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING created_at, updated_at`

	err := t.tx.QueryRowContext(ctx, q,
		transfer.ID, transfer.IdempotencyKey, transfer.FromWalletID,
		transfer.ToWalletID, transfer.Amount, transfer.Currency, transfer.Status,
	).Scan(&transfer.CreatedAt, &transfer.UpdatedAt)
	if err != nil {
		return domain.Transfer{}, fmt.Errorf("create transfer: %w", err)
	}
	return transfer, nil
}

// SetTransferStatus applies a transition, refusing it unless the stored status is
// still from. The guard makes the write itself the thing that settles a transfer
// once, rather than trusting the status the caller last read.
func (t *Tx) SetTransferStatus(
	ctx context.Context, transfer domain.Transfer, from domain.Status,
) (domain.Transfer, error) {
	const q = `
		UPDATE transfers
		SET status = $2, failure_reason = $3, updated_at = now()
		WHERE id = $1 AND status = $4
		RETURNING updated_at`

	var reason *string
	if transfer.FailureReason != "" {
		code := string(transfer.FailureReason)
		reason = &code
	}

	err := t.tx.QueryRowContext(ctx, q, transfer.ID, transfer.Status, reason, from).
		Scan(&transfer.UpdatedAt)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return domain.Transfer{}, domain.ErrInvalidStateTransition
	case err != nil:
		return domain.Transfer{}, fmt.Errorf("set transfer status: %w", err)
	}
	return transfer, nil
}

// TransferByID reads one transfer. A missing row means the idempotency record
// points at a transfer that is not there, which the foreign key should prevent.
func (t *Tx) TransferByID(ctx context.Context, id uuid.UUID) (domain.Transfer, error) {
	const q = `
		SELECT id, idempotency_key, from_wallet_id, to_wallet_id, amount,
		       currency, status, COALESCE(failure_reason, ''), created_at, updated_at
		FROM transfers
		WHERE id = $1`

	transfer, err := scanTransfer(t.tx.QueryRowContext(ctx, q, id))
	if err != nil {
		return domain.Transfer{}, fmt.Errorf("read transfer %s: %w", id, err)
	}
	return transfer, nil
}

// RecordEntries writes the ledger entries for a transfer. The unique constraint
// on (transfer_id, type) rejects a second entry for either side.
func (t *Tx) RecordEntries(ctx context.Context, entries ...domain.LedgerEntry) error {
	const q = `
		INSERT INTO ledger_entries (transfer_id, wallet_id, type, amount)
		VALUES ($1, $2, $3, $4)`

	for _, entry := range entries {
		_, err := t.tx.ExecContext(ctx, q, entry.TransferID, entry.WalletID, entry.Type, entry.Amount)
		if err != nil {
			return fmt.Errorf("record %s entry: %w", entry.Type, err)
		}
	}
	return nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanTransfer(row rowScanner) (domain.Transfer, error) {
	var t domain.Transfer
	err := row.Scan(
		&t.ID, &t.IdempotencyKey, &t.FromWalletID, &t.ToWalletID, &t.Amount,
		&t.Currency, &t.Status, &t.FailureReason, &t.CreatedAt, &t.UpdatedAt,
	)
	return t, err
}
