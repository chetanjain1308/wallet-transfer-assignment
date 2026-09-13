package domain_test

import (
	"errors"
	"testing"

	"github.com/Robustrade/wallet-transfer-assignment/internal/domain"
)

func TestPendingTransferCanSettleEitherWay(t *testing.T) {
	t.Parallel()

	processed := pendingTransfer()
	if err := processed.Process(); err != nil {
		t.Fatalf("Process() on a pending transfer: %v", err)
	}
	if processed.Status != domain.StatusProcessed {
		t.Errorf("status = %q, want PROCESSED", processed.Status)
	}

	failed := pendingTransfer()
	if err := failed.Fail(domain.FailureInsufficientFunds); err != nil {
		t.Fatalf("Fail() on a pending transfer: %v", err)
	}
	if failed.Status != domain.StatusFailed {
		t.Errorf("status = %q, want FAILED", failed.Status)
	}
	if failed.FailureReason != domain.FailureInsufficientFunds {
		t.Errorf("failure reason = %q, want INSUFFICIENT_FUNDS", failed.FailureReason)
	}
}

// A settled transfer must stay settled: this is what stops a duplicate delivery
// from moving money a second time even if it gets past the idempotency check.
func TestSettledTransferCannotTransitionAgain(t *testing.T) {
	t.Parallel()

	tests := map[string]func(*domain.Transfer) error{
		"process a processed transfer": func(tr *domain.Transfer) error { return tr.Process() },
		"fail a processed transfer": func(tr *domain.Transfer) error {
			return tr.Fail(domain.FailureInsufficientFunds)
		},
	}

	for name, transition := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			transfer := pendingTransfer()
			if err := transfer.Process(); err != nil {
				t.Fatalf("setup: %v", err)
			}
			if err := transition(&transfer); !errors.Is(err, domain.ErrInvalidStateTransition) {
				t.Errorf("err = %v, want ErrInvalidStateTransition", err)
			}
		})
	}
}

func TestFailedTransferIsTerminal(t *testing.T) {
	t.Parallel()

	transfer := pendingTransfer()
	if err := transfer.Fail(domain.FailureCurrencyMismatch); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := transfer.Process(); !errors.Is(err, domain.ErrInvalidStateTransition) {
		t.Errorf("err = %v, want ErrInvalidStateTransition", err)
	}
	if !domain.StatusFailed.IsTerminal() {
		t.Error("FAILED should be terminal")
	}
}

func TestRequestValidation(t *testing.T) {
	t.Parallel()

	valid := domain.TransferRequest{
		IdempotencyKey: "key-1",
		FromWalletID:   "wallet_1",
		ToWalletID:     "wallet_2",
		Amount:         100,
	}

	tests := map[string]struct {
		mutate func(*domain.TransferRequest)
		want   error
	}{
		"valid request":      {func(*domain.TransferRequest) {}, nil},
		"no idempotency key": {func(r *domain.TransferRequest) { r.IdempotencyKey = "" }, domain.ErrMissingIdempotencyKey},
		"no source":          {func(r *domain.TransferRequest) { r.FromWalletID = "" }, domain.ErrMissingWalletID},
		"no destination":     {func(r *domain.TransferRequest) { r.ToWalletID = "" }, domain.ErrMissingWalletID},
		"same wallet":        {func(r *domain.TransferRequest) { r.ToWalletID = r.FromWalletID }, domain.ErrSameWallet},
		"zero amount":        {func(r *domain.TransferRequest) { r.Amount = 0 }, domain.ErrNonPositiveAmount},
		"negative amount":    {func(r *domain.TransferRequest) { r.Amount = -1 }, domain.ErrNonPositiveAmount},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			request := valid
			tc.mutate(&request)
			if err := request.Validate(); !errors.Is(err, tc.want) {
				t.Errorf("Validate() = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestEntriesForBalanceToZero(t *testing.T) {
	t.Parallel()

	transfer := pendingTransfer()
	debit, credit := domain.EntriesFor(transfer)

	if debit.WalletID != transfer.FromWalletID {
		t.Errorf("debit wallet = %q, want the source", debit.WalletID)
	}
	if credit.WalletID != transfer.ToWalletID {
		t.Errorf("credit wallet = %q, want the destination", credit.WalletID)
	}
	if sum := debit.SignedAmount() + credit.SignedAmount(); sum != 0 {
		t.Errorf("entries sum to %d, want 0", sum)
	}
}

func pendingTransfer() domain.Transfer {
	return domain.NewTransfer(domain.TransferRequest{
		IdempotencyKey: "key-1",
		FromWalletID:   "wallet_1",
		ToWalletID:     "wallet_2",
		Amount:         100,
	}, "USD")
}
