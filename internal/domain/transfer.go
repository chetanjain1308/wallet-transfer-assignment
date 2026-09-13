package domain

import (
	"time"

	"github.com/google/uuid"
)

// Status is a transfer's position in its lifecycle.
type Status string

const (
	StatusPending   Status = "PENDING"
	StatusProcessed Status = "PROCESSED"
	StatusFailed    Status = "FAILED"
)

// transitions lists the states reachable from each state. PENDING is terminalless;
// PROCESSED and FAILED are terminal, which is what makes a replay safe: a second
// delivery can never move a settled transfer.
var transitions = map[Status][]Status{
	StatusPending:   {StatusProcessed, StatusFailed},
	StatusProcessed: {},
	StatusFailed:    {},
}

// CanTransitionTo reports whether next is reachable from s.
func (s Status) CanTransitionTo(next Status) bool {
	for _, allowed := range transitions[s] {
		if allowed == next {
			return true
		}
	}
	return false
}

// IsTerminal reports whether s admits no further transitions.
func (s Status) IsTerminal() bool {
	return len(transitions[s]) == 0
}

// Transfer is one movement of funds between two wallets.
type Transfer struct {
	ID             uuid.UUID
	IdempotencyKey string
	FromWalletID   string
	ToWalletID     string
	Amount         int64
	Currency       string
	Status         Status
	FailureReason  FailureCode
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// TransferRequest is a validated intent to move funds. Currency is not supplied
// by the caller; it is taken from the wallets once they are loaded.
type TransferRequest struct {
	IdempotencyKey string
	FromWalletID   string
	ToWalletID     string
	Amount         int64
}

// Validate checks the request in isolation, before any wallet is read. Only
// properties of the request itself are checked here — anything needing account
// state is decided later and recorded.
func (r TransferRequest) Validate() error {
	switch {
	case r.IdempotencyKey == "":
		return ErrMissingIdempotencyKey
	case r.FromWalletID == "" || r.ToWalletID == "":
		return ErrMissingWalletID
	case r.FromWalletID == r.ToWalletID:
		return ErrSameWallet
	case r.Amount <= 0:
		return ErrNonPositiveAmount
	}
	return nil
}

// NewTransfer starts a transfer in PENDING.
func NewTransfer(r TransferRequest, currency string) Transfer {
	return Transfer{
		ID:             uuid.New(),
		IdempotencyKey: r.IdempotencyKey,
		FromWalletID:   r.FromWalletID,
		ToWalletID:     r.ToWalletID,
		Amount:         r.Amount,
		Currency:       currency,
		Status:         StatusPending,
	}
}

// Process moves the transfer to PROCESSED.
func (t *Transfer) Process() error {
	if !t.Status.CanTransitionTo(StatusProcessed) {
		return ErrInvalidStateTransition
	}
	t.Status = StatusProcessed
	return nil
}

// Fail moves the transfer to FAILED under the given code.
func (t *Transfer) Fail(code FailureCode) error {
	if !t.Status.CanTransitionTo(StatusFailed) {
		return ErrInvalidStateTransition
	}
	t.Status = StatusFailed
	t.FailureReason = code
	return nil
}
