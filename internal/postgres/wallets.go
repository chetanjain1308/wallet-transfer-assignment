package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Robustrade/wallet-transfer-assignment/internal/domain"
)

// uniqueViolation is the SQLSTATE Postgres raises for a duplicate key.
const uniqueViolation = "23505"

// CreateWallet opens a wallet.
func (s *Store) CreateWallet(ctx context.Context, w domain.Wallet) error {
	const q = `INSERT INTO wallets (id, currency, balance) VALUES ($1, $2, $3)`

	_, err := s.db.ExecContext(ctx, q, w.ID, w.Currency, w.Balance)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
			return domain.ErrWalletExists
		}
		return fmt.Errorf("create wallet %q: %w", w.ID, err)
	}
	return nil
}

// WalletByID reads a wallet and its current balance.
func (s *Store) WalletByID(ctx context.Context, id string) (domain.Wallet, error) {
	const q = `
		SELECT id, currency, balance, created_at, updated_at
		FROM wallets
		WHERE id = $1`

	var w domain.Wallet
	err := s.db.QueryRowContext(ctx, q, id).
		Scan(&w.ID, &w.Currency, &w.Balance, &w.CreatedAt, &w.UpdatedAt)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return domain.Wallet{}, domain.ErrWalletNotFound
	case err != nil:
		return domain.Wallet{}, fmt.Errorf("read wallet %q: %w", id, err)
	}
	return w, nil
}

// TransfersForWallet returns transfers on either side of the wallet, newest
// first. Failed attempts are included: they are part of what happened to the
// wallet even though they moved nothing.
func (s *Store) TransfersForWallet(ctx context.Context, walletID string, limit int) ([]domain.Transfer, error) {
	const q = `
		SELECT id, idempotency_key, from_wallet_id, to_wallet_id, amount,
		       currency, status, COALESCE(failure_reason, ''), created_at, updated_at
		FROM transfers
		WHERE from_wallet_id = $1 OR to_wallet_id = $1
		ORDER BY created_at DESC, id DESC
		LIMIT $2`

	rows, err := s.db.QueryContext(ctx, q, walletID, limit)
	if err != nil {
		return nil, fmt.Errorf("read transfers for %q: %w", walletID, err)
	}
	defer rows.Close()

	transfers := make([]domain.Transfer, 0, limit)
	for rows.Next() {
		transfer, err := scanTransfer(rows)
		if err != nil {
			return nil, fmt.Errorf("read transfers for %q: %w", walletID, err)
		}
		transfers = append(transfers, transfer)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read transfers for %q: %w", walletID, err)
	}
	return transfers, nil
}
