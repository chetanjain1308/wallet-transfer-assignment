package handler

import (
	"errors"
	"net/http"

	"github.com/Robustrade/wallet-transfer-assignment/internal/domain"
)

// errCodeInternal is returned for anything unmapped; the detail stays in the log
// rather than the response.
const errCodeInternal = "INTERNAL"

// statusFor maps a domain error onto its HTTP status and stable error code.
//
// The mapping is a pure function of the error, which is what lets a replayed
// request produce the same response as the original without storing it.
func statusFor(err error) (int, string) {
	switch {
	case errors.Is(err, domain.ErrMissingIdempotencyKey):
		return http.StatusBadRequest, "MISSING_IDEMPOTENCY_KEY"
	case errors.Is(err, domain.ErrMissingWalletID):
		return http.StatusBadRequest, "MISSING_WALLET_ID"
	case errors.Is(err, domain.ErrSameWallet):
		return http.StatusBadRequest, "SAME_WALLET"
	case errors.Is(err, domain.ErrNonPositiveAmount):
		return http.StatusBadRequest, "INVALID_AMOUNT"
	case errors.Is(err, domain.ErrInvalidCurrency):
		return http.StatusBadRequest, "INVALID_CURRENCY"
	case errors.Is(err, domain.ErrNegativeBalance):
		return http.StatusBadRequest, "INVALID_BALANCE"
	case errors.Is(err, errMalformedBody):
		return http.StatusBadRequest, "MALFORMED_BODY"

	case errors.Is(err, domain.ErrWalletNotFound):
		return http.StatusNotFound, "WALLET_NOT_FOUND"

	case errors.Is(err, domain.ErrWalletExists):
		return http.StatusConflict, "WALLET_EXISTS"
	case errors.Is(err, domain.ErrIdempotencyKeyReuse):
		return http.StatusConflict, "IDEMPOTENCY_KEY_REUSE"

	// The request was well formed and named real wallets; it is the account
	// state that refuses it, so it is unprocessable rather than malformed.
	case errors.Is(err, domain.ErrInsufficientFunds):
		return http.StatusUnprocessableEntity, string(domain.FailureInsufficientFunds)
	case errors.Is(err, domain.ErrCurrencyMismatch):
		return http.StatusUnprocessableEntity, string(domain.FailureCurrencyMismatch)

	default:
		return http.StatusInternalServerError, errCodeInternal
	}
}

var errMalformedBody = errors.New("request body is not valid JSON")
