package domain

import "errors"

// Rejections. The request never reaches account state, so nothing is persisted
// and a retry is evaluated from scratch.
var (
	ErrMissingIdempotencyKey = errors.New("idempotencyKey is required")
	ErrMissingWalletID       = errors.New("fromWalletId and toWalletId are required")
	ErrSameWallet            = errors.New("cannot transfer to the same wallet")
	ErrNonPositiveAmount     = errors.New("amount must be greater than zero")
	ErrWalletNotFound        = errors.New("wallet not found")
)

// Recorded failures. The request was well formed and named two real wallets, so
// the attempt is persisted as a FAILED transfer and replays return this outcome.
var (
	ErrInsufficientFunds = errors.New("insufficient funds")
	ErrCurrencyMismatch  = errors.New("wallets hold different currencies")
)

// ErrIdempotencyKeyReuse is returned when a key is replayed with a different
// payload. Honouring either payload would be wrong, so the caller is told.
var ErrIdempotencyKeyReuse = errors.New("idempotency key already used with a different request")

// ErrInvalidStateTransition guards the transfer state machine against a
// transition the lifecycle does not allow.
var ErrInvalidStateTransition = errors.New("invalid transfer state transition")

// Wallet creation rejections.
var (
	ErrInvalidCurrency = errors.New("currency must be a three-letter code")
	ErrNegativeBalance = errors.New("balance cannot be negative")
	ErrWalletExists    = errors.New("wallet already exists")
)
