package service_test

import (
	"context"
	"errors"
	"testing"

	"github.com/Robustrade/wallet-transfer-assignment/internal/domain"
	"github.com/Robustrade/wallet-transfer-assignment/internal/service"
	"github.com/Robustrade/wallet-transfer-assignment/internal/testsupport"
)

func TestTransferMovesFundsAndRecordsBothSides(t *testing.T) {
	t.Parallel()

	h := testsupport.New(t)
	transfers := service.NewTransferService(h.Store, testsupport.Logger())
	source := h.Wallet("USD", 1_000)
	destination := h.Wallet("USD", 250)

	result, err := transfers.Transfer(context.Background(), domain.TransferRequest{
		IdempotencyKey: testsupport.Key(),
		FromWalletID:   source,
		ToWalletID:     destination,
		Amount:         400,
	})
	if err != nil {
		t.Fatalf("Transfer() = %v", err)
	}
	if result.Failure != nil {
		t.Fatalf("unexpected failure: %v", result.Failure)
	}

	if result.Transfer.Status != domain.StatusProcessed {
		t.Errorf("status = %q, want PROCESSED", result.Transfer.Status)
	}
	if got := h.Balance(source); got != 600 {
		t.Errorf("source balance = %d, want 600", got)
	}
	if got := h.Balance(destination); got != 650 {
		t.Errorf("destination balance = %d, want 650", got)
	}
	if got := h.EntryCount(result.Transfer.ID); got != 2 {
		t.Errorf("ledger entries = %d, want 2", got)
	}
	if got := h.LedgerBalance(source) + h.LedgerBalance(destination); got != 0 {
		t.Errorf("ledger sums to %d across both wallets, want 0", got)
	}
}

func TestReplayReturnsTheOriginalTransfer(t *testing.T) {
	t.Parallel()

	h := testsupport.New(t)
	transfers := service.NewTransferService(h.Store, testsupport.Logger())
	source := h.Wallet("USD", 1_000)
	destination := h.Wallet("USD", 0)

	request := domain.TransferRequest{
		IdempotencyKey: testsupport.Key(),
		FromWalletID:   source,
		ToWalletID:     destination,
		Amount:         300,
	}

	first, err := transfers.Transfer(context.Background(), request)
	if err != nil {
		t.Fatalf("first Transfer() = %v", err)
	}

	second, err := transfers.Transfer(context.Background(), request)
	if err != nil {
		t.Fatalf("replayed Transfer() = %v", err)
	}

	if !second.Replayed {
		t.Error("second request should be marked as a replay")
	}
	if second.Transfer.ID != first.Transfer.ID {
		t.Errorf("replay returned transfer %s, want the original %s", second.Transfer.ID, first.Transfer.ID)
	}
	if got := h.Balance(source); got != 700 {
		t.Errorf("source balance = %d, want 700 — the duplicate moved money again", got)
	}
	if got := h.TransferCountForKey(request.IdempotencyKey); got != 1 {
		t.Errorf("transfers for the key = %d, want 1", got)
	}
	if got := h.EntryCount(first.Transfer.ID); got != 2 {
		t.Errorf("ledger entries = %d, want 2", got)
	}
}

func TestReusingAKeyWithDifferentTermsIsRefused(t *testing.T) {
	t.Parallel()

	h := testsupport.New(t)
	transfers := service.NewTransferService(h.Store, testsupport.Logger())
	source := h.Wallet("USD", 1_000)
	destination := h.Wallet("USD", 0)

	request := domain.TransferRequest{
		IdempotencyKey: testsupport.Key(),
		FromWalletID:   source,
		ToWalletID:     destination,
		Amount:         100,
	}
	if _, err := transfers.Transfer(context.Background(), request); err != nil {
		t.Fatalf("first Transfer() = %v", err)
	}

	request.Amount = 900
	_, err := transfers.Transfer(context.Background(), request)
	if !errors.Is(err, domain.ErrIdempotencyKeyReuse) {
		t.Fatalf("Transfer() = %v, want ErrIdempotencyKeyReuse", err)
	}
	if got := h.Balance(source); got != 900 {
		t.Errorf("source balance = %d, want 900", got)
	}
}

func TestInsufficientFundsIsRecordedAndReplayable(t *testing.T) {
	t.Parallel()

	h := testsupport.New(t)
	transfers := service.NewTransferService(h.Store, testsupport.Logger())
	source := h.Wallet("USD", 50)
	destination := h.Wallet("USD", 0)

	request := domain.TransferRequest{
		IdempotencyKey: testsupport.Key(),
		FromWalletID:   source,
		ToWalletID:     destination,
		Amount:         100,
	}

	result, err := transfers.Transfer(context.Background(), request)
	if err != nil {
		t.Fatalf("Transfer() = %v", err)
	}
	if !errors.Is(result.Failure, domain.ErrInsufficientFunds) {
		t.Fatalf("failure = %v, want ErrInsufficientFunds", result.Failure)
	}
	if result.Transfer.Status != domain.StatusFailed {
		t.Errorf("status = %q, want FAILED", result.Transfer.Status)
	}
	if got := h.EntryCount(result.Transfer.ID); got != 0 {
		t.Errorf("ledger entries = %d, want 0 for a failed transfer", got)
	}
	if got := h.Balance(source); got != 50 {
		t.Errorf("source balance = %d, want 50", got)
	}

	replay, err := transfers.Transfer(context.Background(), request)
	if err != nil {
		t.Fatalf("replayed Transfer() = %v", err)
	}
	if !errors.Is(replay.Failure, domain.ErrInsufficientFunds) {
		t.Errorf("replayed failure = %v, want ErrInsufficientFunds", replay.Failure)
	}
	if replay.Transfer.ID != result.Transfer.ID {
		t.Error("replay should return the original failed transfer")
	}
}

func TestMismatchedCurrenciesAreRecordedAsFailed(t *testing.T) {
	t.Parallel()

	h := testsupport.New(t)
	transfers := service.NewTransferService(h.Store, testsupport.Logger())
	source := h.Wallet("USD", 1_000)
	destination := h.Wallet("EUR", 0)

	result, err := transfers.Transfer(context.Background(), domain.TransferRequest{
		IdempotencyKey: testsupport.Key(),
		FromWalletID:   source,
		ToWalletID:     destination,
		Amount:         100,
	})
	if err != nil {
		t.Fatalf("Transfer() = %v", err)
	}
	if !errors.Is(result.Failure, domain.ErrCurrencyMismatch) {
		t.Fatalf("failure = %v, want ErrCurrencyMismatch", result.Failure)
	}
	if got := h.Balance(source); got != 1_000 {
		t.Errorf("source balance = %d, want 1000", got)
	}
}

// An unknown wallet cannot be recorded as a failed transfer: the foreign key has
// nothing to point at. It is rejected outright and leaves no trace, so a retry is
// evaluated afresh rather than replaying a refusal.
func TestUnknownWalletIsRejectedWithoutPersisting(t *testing.T) {
	t.Parallel()

	h := testsupport.New(t)
	transfers := service.NewTransferService(h.Store, testsupport.Logger())
	source := h.Wallet("USD", 1_000)

	key := testsupport.Key()
	_, err := transfers.Transfer(context.Background(), domain.TransferRequest{
		IdempotencyKey: key,
		FromWalletID:   source,
		ToWalletID:     "wallet_does_not_exist",
		Amount:         100,
	})
	if !errors.Is(err, domain.ErrWalletNotFound) {
		t.Fatalf("Transfer() = %v, want ErrWalletNotFound", err)
	}
	if got := h.TransferCountForKey(key); got != 0 {
		t.Errorf("transfers stored = %d, want 0", got)
	}
}

func TestInvalidRequestsAreRejectedBeforeAnyWrite(t *testing.T) {
	t.Parallel()

	h := testsupport.New(t)
	transfers := service.NewTransferService(h.Store, testsupport.Logger())
	wallet := h.Wallet("USD", 1_000)

	tests := map[string]struct {
		request domain.TransferRequest
		want    error
	}{
		"same wallet": {
			domain.TransferRequest{IdempotencyKey: testsupport.Key(), FromWalletID: wallet, ToWalletID: wallet, Amount: 10},
			domain.ErrSameWallet,
		},
		"zero amount": {
			domain.TransferRequest{IdempotencyKey: testsupport.Key(), FromWalletID: wallet, ToWalletID: "other", Amount: 0},
			domain.ErrNonPositiveAmount,
		},
		"no key": {
			domain.TransferRequest{FromWalletID: wallet, ToWalletID: "other", Amount: 10},
			domain.ErrMissingIdempotencyKey,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if _, err := transfers.Transfer(context.Background(), tc.request); !errors.Is(err, tc.want) {
				t.Errorf("Transfer() = %v, want %v", err, tc.want)
			}
		})
	}
}
