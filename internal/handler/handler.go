// Package handler exposes the service over HTTP. It validates transport, maps
// errors onto status codes, and holds no business rules of its own.
package handler

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/Robustrade/wallet-transfer-assignment/internal/domain"
	"github.com/Robustrade/wallet-transfer-assignment/internal/service"
)

// replayHeader marks a response served from an earlier identical request.
const replayHeader = "Idempotent-Replay"

// maxBodyBytes caps a request body; the payloads here are a few hundred bytes.
const maxBodyBytes = 8 << 10

// Handler serves the wallet API.
type Handler struct {
	transfers *service.TransferService
	wallets   *service.WalletService
	log       *slog.Logger
}

// New builds a Handler.
func New(transfers *service.TransferService, wallets *service.WalletService, log *slog.Logger) *Handler {
	return &Handler{transfers: transfers, wallets: wallets, log: log}
}

// Routes returns the API with middleware applied.
func (h *Handler) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /transfers", h.createTransfer)
	mux.HandleFunc("POST /wallets", h.createWallet)
	mux.HandleFunc("GET /wallets/{id}", h.getWallet)
	mux.HandleFunc("GET /wallets/{id}/transfers", h.listTransfers)
	mux.HandleFunc("GET /healthz", h.health)

	return requestID(accessLog(h.log, recoverPanic(h.log, mux)))
}

func (h *Handler) createTransfer(w http.ResponseWriter, r *http.Request) {
	var body createTransferRequest
	if err := decode(w, r, &body); err != nil {
		h.writeError(w, r, err, nil)
		return
	}

	result, err := h.transfers.Transfer(r.Context(), body.toDomain())
	if err != nil {
		h.writeError(w, r, err, nil)
		return
	}

	if result.Replayed {
		w.Header().Set(replayHeader, "true")
	}
	if result.Failure != nil {
		response := newTransferResponse(result.Transfer)
		h.writeError(w, r, result.Failure, &response)
		return
	}

	// A replay reports the status the original request produced, so a caller that
	// retried a lost response sees exactly what it would have seen the first time.
	writeJSON(w, http.StatusCreated, newTransferResponse(result.Transfer))
}

func (h *Handler) createWallet(w http.ResponseWriter, r *http.Request) {
	var body createWalletRequest
	if err := decode(w, r, &body); err != nil {
		h.writeError(w, r, err, nil)
		return
	}

	wallet, err := h.wallets.Create(r.Context(), domain.Wallet{
		ID:       body.ID,
		Currency: body.Currency,
		Balance:  body.Balance,
	})
	if err != nil {
		h.writeError(w, r, err, nil)
		return
	}
	writeJSON(w, http.StatusCreated, newWalletResponse(wallet))
}

func (h *Handler) getWallet(w http.ResponseWriter, r *http.Request) {
	wallet, err := h.wallets.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		h.writeError(w, r, err, nil)
		return
	}
	writeJSON(w, http.StatusOK, newWalletResponse(wallet))
}

func (h *Handler) listTransfers(w http.ResponseWriter, r *http.Request) {
	limit, err := strconv.Atoi(r.URL.Query().Get("limit"))
	if err != nil {
		limit = 0 // the service applies its default
	}

	transfers, err := h.wallets.History(r.Context(), r.PathValue("id"), limit)
	if err != nil {
		h.writeError(w, r, err, nil)
		return
	}

	response := transferListResponse{Transfers: make([]transferResponse, 0, len(transfers))}
	for _, transfer := range transfers {
		response.Transfers = append(response.Transfers, newTransferResponse(transfer))
	}
	writeJSON(w, http.StatusOK, response)
}

func (h *Handler) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *Handler) writeError(w http.ResponseWriter, r *http.Request, err error, transfer *transferResponse) {
	status, code := statusFor(err)

	message := err.Error()
	if status == http.StatusInternalServerError {
		h.log.ErrorContext(r.Context(), "request failed", "error", err, "path", r.URL.Path)
		message = "internal error"
	}

	writeJSON(w, status, errorResponse{
		Error:    errorBody{Code: code, Message: message},
		Transfer: transfer,
	})
}

func decode(w http.ResponseWriter, r *http.Request, target any) error {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return errors.Join(errMalformedBody, err)
	}
	// Decode stops after one value, so without this a second object or trailing
	// garbage would be ignored and the request executed anyway.
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.Join(errMalformedBody, errTrailingData)
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	// The response is already committed by WriteHeader, so a failed encode can
	// only be logged by the caller's access log via the status already sent.
	_ = json.NewEncoder(w).Encode(body)
}
