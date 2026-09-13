package handler

import (
	"time"

	"github.com/Robustrade/wallet-transfer-assignment/internal/domain"
)

type createTransferRequest struct {
	IdempotencyKey string `json:"idempotencyKey"`
	FromWalletID   string `json:"fromWalletId"`
	ToWalletID     string `json:"toWalletId"`
	Amount         int64  `json:"amount"`
}

func (r createTransferRequest) toDomain() domain.TransferRequest {
	return domain.TransferRequest{
		IdempotencyKey: r.IdempotencyKey,
		FromWalletID:   r.FromWalletID,
		ToWalletID:     r.ToWalletID,
		Amount:         r.Amount,
	}
}

type transferResponse struct {
	ID             string    `json:"id"`
	IdempotencyKey string    `json:"idempotencyKey"`
	FromWalletID   string    `json:"fromWalletId"`
	ToWalletID     string    `json:"toWalletId"`
	Amount         int64     `json:"amount"`
	Currency       string    `json:"currency"`
	Status         string    `json:"status"`
	FailureReason  string    `json:"failureReason,omitempty"`
	CreatedAt      time.Time `json:"createdAt"`
}

func newTransferResponse(t domain.Transfer) transferResponse {
	return transferResponse{
		ID:             t.ID.String(),
		IdempotencyKey: t.IdempotencyKey,
		FromWalletID:   t.FromWalletID,
		ToWalletID:     t.ToWalletID,
		Amount:         t.Amount,
		Currency:       t.Currency,
		Status:         string(t.Status),
		FailureReason:  string(t.FailureReason),
		CreatedAt:      t.CreatedAt,
	}
}

type createWalletRequest struct {
	ID       string `json:"id"`
	Currency string `json:"currency"`
	Balance  int64  `json:"balance"`
}

type walletResponse struct {
	ID       string `json:"id"`
	Currency string `json:"currency"`
	Balance  int64  `json:"balance"`
}

func newWalletResponse(w domain.Wallet) walletResponse {
	return walletResponse{ID: w.ID, Currency: w.Currency, Balance: w.Balance}
}

type transferListResponse struct {
	Transfers []transferResponse `json:"transfers"`
}

// errorResponse carries a stable machine-readable code alongside the message.
// A failed transfer also returns the transfer itself, since the attempt is part
// of the wallet's history and the caller may want its id.
type errorResponse struct {
	Error    errorBody         `json:"error"`
	Transfer *transferResponse `json:"transfer,omitempty"`
}

type errorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}
