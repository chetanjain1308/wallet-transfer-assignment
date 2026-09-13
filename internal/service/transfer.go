package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"

	"github.com/Robustrade/wallet-transfer-assignment/internal/domain"
)

// TransferService runs the transfer workflow: claim the idempotency key, settle
// the money, record both sides of the ledger.
type TransferService struct {
	store Store
	log   *slog.Logger
}

// NewTransferService wires the service to its store.
func NewTransferService(store Store, log *slog.Logger) *TransferService {
	return &TransferService{store: store, log: log}
}

// Result is the outcome of a transfer request.
//
// Failure separates a committed refusal from an aborted one. When it is set the
// Transfer exists in FAILED and the attempt is part of the wallet's history;
// when Transfer returns an error instead, nothing was persisted at all. Replayed
// marks a result served from an earlier request rather than executed again.
type Result struct {
	Transfer domain.Transfer
	Replayed bool
	Failure  error
}

// Transfer executes req, or returns the result of the earlier request that used
// the same idempotency key.
func (s *TransferService) Transfer(ctx context.Context, req domain.TransferRequest) (Result, error) {
	if err := req.Validate(); err != nil {
		return Result{}, err
	}

	fingerprint := fingerprintOf(req)

	var result Result
	err := s.store.Atomic(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		result, err = s.execute(ctx, tx, req, fingerprint)
		return err
	})
	if err != nil {
		return Result{}, err
	}
	return result, nil
}

func (s *TransferService) execute(
	ctx context.Context, tx Tx, req domain.TransferRequest, fingerprint string,
) (Result, error) {
	claimed, err := tx.ClaimIdempotencyKey(ctx, req.IdempotencyKey, fingerprint)
	if err != nil {
		return Result{}, err
	}
	if !claimed {
		return s.replay(ctx, tx, req, fingerprint)
	}
	return s.settle(ctx, tx, req)
}

// replay serves a request whose key was already claimed. The transfer the key
// points at is authoritative, so the outcome is rebuilt from the stored row
// rather than from a copy of the first response.
func (s *TransferService) replay(
	ctx context.Context, tx Tx, req domain.TransferRequest, fingerprint string,
) (Result, error) {
	record, err := tx.IdempotencyRecord(ctx, req.IdempotencyKey)
	if err != nil {
		return Result{}, err
	}
	if record.RequestFingerprint != fingerprint {
		return Result{}, domain.ErrIdempotencyKeyReuse
	}

	transfer, err := tx.TransferByID(ctx, record.TransferID)
	if err != nil {
		return Result{}, err
	}

	s.log.InfoContext(ctx, "transfer replayed",
		"idempotency_key", req.IdempotencyKey,
		"transfer_id", transfer.ID,
		"status", transfer.Status,
	)

	result := Result{Transfer: transfer, Replayed: true}
	if transfer.Status == domain.StatusFailed {
		result.Failure = transfer.FailureReason.Err()
	}
	return result, nil
}

// settle performs a first-time transfer. Both wallets are locked before any
// balance is read, so the check-then-write below cannot interleave with another
// transfer touching either wallet.
func (s *TransferService) settle(ctx context.Context, tx Tx, req domain.TransferRequest) (Result, error) {
	wallets, err := tx.LockWallets(ctx, req.FromWalletID, req.ToWalletID)
	if err != nil {
		return Result{}, err
	}
	source, destination := wallets[req.FromWalletID], wallets[req.ToWalletID]

	transfer, err := tx.CreateTransfer(ctx, domain.NewTransfer(req, source.Currency))
	if err != nil {
		return Result{}, err
	}
	if err := tx.LinkIdempotencyKey(ctx, req.IdempotencyKey, transfer.ID); err != nil {
		return Result{}, err
	}

	if code, failed := failureFor(source, destination, req.Amount); failed {
		return s.recordFailure(ctx, tx, transfer, code)
	}

	debit, credit := domain.EntriesFor(transfer)
	if err := tx.RecordEntries(ctx, debit, credit); err != nil {
		return Result{}, err
	}
	if err := tx.AdjustBalance(ctx, source.ID, -transfer.Amount); err != nil {
		return Result{}, err
	}
	if err := tx.AdjustBalance(ctx, destination.ID, transfer.Amount); err != nil {
		return Result{}, err
	}

	if err := transfer.Process(); err != nil {
		return Result{}, err
	}
	transfer, err = tx.SetTransferStatus(ctx, transfer, domain.StatusPending)
	if err != nil {
		return Result{}, err
	}

	s.log.InfoContext(ctx, "transfer processed",
		"idempotency_key", req.IdempotencyKey,
		"transfer_id", transfer.ID,
		"from_wallet_id", transfer.FromWalletID,
		"to_wallet_id", transfer.ToWalletID,
		"amount", transfer.Amount,
	)
	return Result{Transfer: transfer}, nil
}

// recordFailure commits the attempt as FAILED. The transfer is kept so the
// wallet's history shows it, and so replaying the key returns this same answer.
func (s *TransferService) recordFailure(
	ctx context.Context, tx Tx, transfer domain.Transfer, code domain.FailureCode,
) (Result, error) {
	if err := transfer.Fail(code); err != nil {
		return Result{}, err
	}

	transfer, err := tx.SetTransferStatus(ctx, transfer, domain.StatusPending)
	if err != nil {
		return Result{}, err
	}

	s.log.InfoContext(ctx, "transfer failed",
		"idempotency_key", transfer.IdempotencyKey,
		"transfer_id", transfer.ID,
		"reason", code,
	)
	return Result{Transfer: transfer, Failure: code.Err()}, nil
}

// failureFor reports the failure code the wallets dictate, if any.
func failureFor(source, destination domain.Wallet, amount int64) (domain.FailureCode, bool) {
	if source.Currency != destination.Currency {
		return domain.FailureCurrencyMismatch, true
	}
	if !source.CanDebit(amount) {
		return domain.FailureInsufficientFunds, true
	}
	return "", false
}

// fingerprintOf identifies the request a key was first used for, so that reusing a
// key on different terms is caught instead of silently returning the wrong
// transfer.
func fingerprintOf(req domain.TransferRequest) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%s|%d",
		req.FromWalletID, req.ToWalletID, req.Amount)))
	return hex.EncodeToString(sum[:])
}
