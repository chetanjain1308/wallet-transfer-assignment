package service_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/Robustrade/wallet-transfer-assignment/internal/domain"
	"github.com/Robustrade/wallet-transfer-assignment/internal/service"
	"github.com/Robustrade/wallet-transfer-assignment/internal/testsupport"
)

// Twenty concurrent debits against a wallet that can only fund ten of them. The
// row lock is what decides: without it the balance check and the balance write
// interleave, and more than ten would be allowed through.
func TestConcurrentDebitsCannotOverdraw(t *testing.T) {
	t.Parallel()

	const (
		attempts = 20
		amount   = 100
		funded   = 1_000
	)

	h := testsupport.New(t)
	transfers := service.NewTransferService(h.Store, testsupport.Logger())
	source := h.Wallet("USD", funded)
	destination := h.Wallet("USD", 0)

	results := runConcurrently(t, attempts, func(int) (service.Result, error) {
		return transfers.Transfer(context.Background(), domain.TransferRequest{
			IdempotencyKey: testsupport.Key(),
			FromWalletID:   source,
			ToWalletID:     destination,
			Amount:         amount,
		})
	})

	var processed, insufficient int
	for _, r := range results {
		if r.err != nil {
			t.Fatalf("Transfer() = %v", r.err)
		}
		switch {
		case r.result.Failure == nil:
			processed++
			if got := h.EntryCount(r.result.Transfer.ID); got != 2 {
				t.Errorf("processed transfer %s has %d entries, want 2", r.result.Transfer.ID, got)
			}
		case errors.Is(r.result.Failure, domain.ErrInsufficientFunds):
			insufficient++
			if got := h.EntryCount(r.result.Transfer.ID); got != 0 {
				t.Errorf("failed transfer %s has %d entries, want 0", r.result.Transfer.ID, got)
			}
		default:
			t.Fatalf("unexpected failure: %v", r.result.Failure)
		}
	}

	if processed != funded/amount {
		t.Errorf("processed = %d, want %d", processed, funded/amount)
	}
	if insufficient != attempts-funded/amount {
		t.Errorf("rejected for funds = %d, want %d", insufficient, attempts-funded/amount)
	}
	if got := h.Balance(source); got != 0 {
		t.Errorf("source balance = %d, want 0", got)
	}
	if got := h.Balance(destination); got != funded {
		t.Errorf("destination balance = %d, want %d", got, funded)
	}
	if got := h.LedgerBalance(source) + h.LedgerBalance(destination); got != 0 {
		t.Errorf("ledger sums to %d, want 0", got)
	}
}

// The same key delivered many times at once. Exactly one caller may do the work;
// the rest must block on the key and return that caller's transfer.
func TestConcurrentDuplicatesProduceOneTransfer(t *testing.T) {
	t.Parallel()

	const attempts = 20

	h := testsupport.New(t)
	transfers := service.NewTransferService(h.Store, testsupport.Logger())
	source := h.Wallet("USD", 1_000)
	destination := h.Wallet("USD", 0)

	key := testsupport.Key()
	results := runConcurrently(t, attempts, func(int) (service.Result, error) {
		return transfers.Transfer(context.Background(), domain.TransferRequest{
			IdempotencyKey: key,
			FromWalletID:   source,
			ToWalletID:     destination,
			Amount:         250,
		})
	})

	var executed int
	transferID := results[0].result.Transfer.ID
	for _, r := range results {
		if r.err != nil {
			t.Fatalf("Transfer() = %v", r.err)
		}
		if r.result.Transfer.ID != transferID {
			t.Errorf("got transfer %s, want every caller to see %s", r.result.Transfer.ID, transferID)
		}
		if !r.result.Replayed {
			executed++
		}
	}

	if executed != 1 {
		t.Errorf("%d callers executed the transfer, want exactly 1", executed)
	}
	if got := h.TransferCountForKey(key); got != 1 {
		t.Errorf("transfers stored for the key = %d, want 1", got)
	}
	if got := h.EntryCount(transferID); got != 2 {
		t.Errorf("ledger entries = %d, want 2", got)
	}
	if got := h.Balance(source); got != 750 {
		t.Errorf("source balance = %d, want 750", got)
	}
}

// Opposing transfers between the same pair. Locking the wallets in id order is
// what keeps this from deadlocking; without it Postgres aborts one side.
func TestOpposingTransfersDoNotDeadlock(t *testing.T) {
	t.Parallel()

	const pairs = 25

	h := testsupport.New(t)
	transfers := service.NewTransferService(h.Store, testsupport.Logger())
	left := h.Wallet("USD", 5_000)
	right := h.Wallet("USD", 5_000)

	results := runConcurrently(t, pairs*2, func(i int) (service.Result, error) {
		from, to := left, right
		if i%2 == 1 {
			from, to = right, left
		}
		return transfers.Transfer(context.Background(), domain.TransferRequest{
			IdempotencyKey: testsupport.Key(),
			FromWalletID:   from,
			ToWalletID:     to,
			Amount:         10,
		})
	})

	for _, r := range results {
		if r.err != nil {
			t.Fatalf("Transfer() = %v", r.err)
		}
		if r.result.Failure != nil {
			t.Fatalf("unexpected failure: %v", r.result.Failure)
		}
	}

	// Equal traffic both ways leaves both wallets where they started.
	if got := h.Balance(left); got != 5_000 {
		t.Errorf("left balance = %d, want 5000", got)
	}
	if got := h.Balance(right); got != 5_000 {
		t.Errorf("right balance = %d, want 5000", got)
	}
	if got := h.LedgerBalance(left) + h.LedgerBalance(right); got != 0 {
		t.Errorf("ledger sums to %d, want 0", got)
	}
}

type outcome struct {
	result service.Result
	err    error
}

// runConcurrently starts n callers at once and collects what each got back.
func runConcurrently(t *testing.T, n int, call func(i int) (service.Result, error)) []outcome {
	t.Helper()

	var (
		start     sync.WaitGroup
		finished  sync.WaitGroup
		outcomes  = make([]outcome, n)
		beginning = make(chan struct{})
	)
	start.Add(n)
	finished.Add(n)

	for i := range n {
		go func() {
			defer finished.Done()
			start.Done()
			<-beginning

			result, err := call(i)
			outcomes[i] = outcome{result: result, err: err}
		}()
	}

	start.Wait()
	close(beginning)
	finished.Wait()

	return outcomes
}
