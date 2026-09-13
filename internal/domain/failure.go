package domain

import "errors"

// FailureCode is the stored reason a transfer reached FAILED. It is persisted
// rather than the error text so that replaying a failed transfer reconstructs
// the same outcome, and so wording changes never alter a recorded result.
type FailureCode string

const (
	FailureInsufficientFunds FailureCode = "INSUFFICIENT_FUNDS"
	FailureCurrencyMismatch  FailureCode = "CURRENCY_MISMATCH"
)

// Err returns the error a failure code stands for.
func (c FailureCode) Err() error {
	switch c {
	case FailureInsufficientFunds:
		return ErrInsufficientFunds
	case FailureCurrencyMismatch:
		return ErrCurrencyMismatch
	default:
		return ErrInvalidStateTransition
	}
}

// FailureCodeFor returns the code under which err is recorded, and whether err
// is a failure that gets recorded at all.
func FailureCodeFor(err error) (FailureCode, bool) {
	switch {
	case errors.Is(err, ErrInsufficientFunds):
		return FailureInsufficientFunds, true
	case errors.Is(err, ErrCurrencyMismatch):
		return FailureCurrencyMismatch, true
	default:
		return "", false
	}
}
