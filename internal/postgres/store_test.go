package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/Robustrade/wallet-transfer-assignment/internal/service"
	"github.com/Robustrade/wallet-transfer-assignment/internal/testsupport"
)

// A panic inside the callback must still resolve the transaction. Left open it
// holds the row locks its statements took, so the next transfer touching either
// wallet blocks until its context expires.
func TestAtomicRollsBackWhenTheCallbackPanics(t *testing.T) {
	t.Parallel()

	h := testsupport.New(t)
	source := h.Wallet("USD", 100)
	destination := h.Wallet("USD", 0)

	func() {
		defer func() {
			if recovered := recover(); recovered == nil {
				t.Error("the panic should reach the caller")
			}
		}()

		_ = h.Store.Atomic(context.Background(), func(ctx context.Context, tx service.Tx) error {
			// Takes a row lock on source, which is what a leaked transaction holds.
			if err := tx.AdjustBalance(ctx, source, -50); err != nil {
				return err
			}
			panic("callback exploded")
		})
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := h.Store.Atomic(ctx, func(ctx context.Context, tx service.Tx) error {
		_, err := tx.LockWallets(ctx, source, destination)
		return err
	})
	if err != nil {
		t.Fatalf("could not lock the wallet after the panic, so the transaction was left open: %v", err)
	}

	if got := h.Balance(source); got != 100 {
		t.Errorf("balance = %d, want 100 — the panicking transaction was committed", got)
	}
}
