package handler_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Robustrade/wallet-transfer-assignment/internal/handler"
	"github.com/Robustrade/wallet-transfer-assignment/internal/service"
	"github.com/Robustrade/wallet-transfer-assignment/internal/testsupport"
)

func TestTransferEndpointReturnsTheCreatedTransfer(t *testing.T) {
	t.Parallel()

	api, h := newAPI(t)
	source := h.Wallet("USD", 1_000)
	destination := h.Wallet("USD", 0)

	response := post(t, api, "/transfers", map[string]any{
		"idempotencyKey": testsupport.Key(),
		"fromWalletId":   source,
		"toWalletId":     destination,
		"amount":         400,
	})

	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %s", response.Code, response.Body)
	}

	var body map[string]any
	decodeBody(t, response, &body)
	if body["status"] != "PROCESSED" {
		t.Errorf("status = %v, want PROCESSED", body["status"])
	}
	if body["currency"] != "USD" {
		t.Errorf("currency = %v, want USD taken from the wallets", body["currency"])
	}
}

// A caller that never saw the first response retries and must get that response
// back, marked so it can tell the difference if it cares.
func TestReplayReturnsTheSameResponse(t *testing.T) {
	t.Parallel()

	api, h := newAPI(t)
	source := h.Wallet("USD", 1_000)
	destination := h.Wallet("USD", 0)

	request := map[string]any{
		"idempotencyKey": testsupport.Key(),
		"fromWalletId":   source,
		"toWalletId":     destination,
		"amount":         400,
	}

	first := post(t, api, "/transfers", request)
	second := post(t, api, "/transfers", request)

	if first.Code != second.Code {
		t.Errorf("status codes differ: %d then %d", first.Code, second.Code)
	}
	if first.Body.String() != second.Body.String() {
		t.Errorf("bodies differ:\n%s\n%s", first.Body, second.Body)
	}
	if got := second.Header().Get("Idempotent-Replay"); got != "true" {
		t.Errorf("Idempotent-Replay = %q, want true", got)
	}
	if got := first.Header().Get("Idempotent-Replay"); got != "" {
		t.Errorf("the original response should not be marked a replay, got %q", got)
	}
}

func TestErrorMapping(t *testing.T) {
	t.Parallel()

	api, h := newAPI(t)
	funded := h.Wallet("USD", 100)
	empty := h.Wallet("USD", 0)
	euros := h.Wallet("EUR", 0)

	tests := map[string]struct {
		body       map[string]any
		wantStatus int
		wantCode   string
	}{
		"insufficient funds": {
			map[string]any{"idempotencyKey": testsupport.Key(), "fromWalletId": empty, "toWalletId": funded, "amount": 50},
			http.StatusUnprocessableEntity, "INSUFFICIENT_FUNDS",
		},
		"currency mismatch": {
			map[string]any{"idempotencyKey": testsupport.Key(), "fromWalletId": funded, "toWalletId": euros, "amount": 50},
			http.StatusUnprocessableEntity, "CURRENCY_MISMATCH",
		},
		"unknown wallet": {
			map[string]any{"idempotencyKey": testsupport.Key(), "fromWalletId": funded, "toWalletId": "nope", "amount": 50},
			http.StatusNotFound, "WALLET_NOT_FOUND",
		},
		"same wallet": {
			map[string]any{"idempotencyKey": testsupport.Key(), "fromWalletId": funded, "toWalletId": funded, "amount": 50},
			http.StatusBadRequest, "SAME_WALLET",
		},
		"negative amount": {
			map[string]any{"idempotencyKey": testsupport.Key(), "fromWalletId": funded, "toWalletId": empty, "amount": -1},
			http.StatusBadRequest, "INVALID_AMOUNT",
		},
		"missing key": {
			map[string]any{"fromWalletId": funded, "toWalletId": empty, "amount": 50},
			http.StatusBadRequest, "MISSING_IDEMPOTENCY_KEY",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			response := post(t, api, "/transfers", tc.body)
			if response.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d: %s", response.Code, tc.wantStatus, response.Body)
			}

			var body struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			decodeBody(t, response, &body)
			if body.Error.Code != tc.wantCode {
				t.Errorf("error code = %q, want %q", body.Error.Code, tc.wantCode)
			}
		})
	}
}

// A refused transfer still returns its transfer id, because the attempt is part
// of the wallet history.
func TestFailedTransferResponseCarriesTheTransfer(t *testing.T) {
	t.Parallel()

	api, h := newAPI(t)
	source := h.Wallet("USD", 10)
	destination := h.Wallet("USD", 0)

	response := post(t, api, "/transfers", map[string]any{
		"idempotencyKey": testsupport.Key(),
		"fromWalletId":   source,
		"toWalletId":     destination,
		"amount":         500,
	})

	var body struct {
		Transfer struct {
			ID            string `json:"id"`
			Status        string `json:"status"`
			FailureReason string `json:"failureReason"`
		} `json:"transfer"`
	}
	decodeBody(t, response, &body)

	if body.Transfer.ID == "" {
		t.Error("a recorded failure should return its transfer id")
	}
	if body.Transfer.Status != "FAILED" {
		t.Errorf("status = %q, want FAILED", body.Transfer.Status)
	}
	if body.Transfer.FailureReason != "INSUFFICIENT_FUNDS" {
		t.Errorf("failure reason = %q, want INSUFFICIENT_FUNDS", body.Transfer.FailureReason)
	}
}

func TestKeyReuseWithDifferentTermsConflicts(t *testing.T) {
	t.Parallel()

	api, h := newAPI(t)
	source := h.Wallet("USD", 1_000)
	destination := h.Wallet("USD", 0)

	request := map[string]any{
		"idempotencyKey": testsupport.Key(),
		"fromWalletId":   source,
		"toWalletId":     destination,
		"amount":         100,
	}
	post(t, api, "/transfers", request)

	request["amount"] = 900
	response := post(t, api, "/transfers", request)

	if response.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409: %s", response.Code, response.Body)
	}
}

func TestWalletBalanceAndHistory(t *testing.T) {
	t.Parallel()

	api, h := newAPI(t)
	source := h.Wallet("USD", 1_000)
	destination := h.Wallet("USD", 0)

	post(t, api, "/transfers", map[string]any{
		"idempotencyKey": testsupport.Key(),
		"fromWalletId":   source,
		"toWalletId":     destination,
		"amount":         250,
	})

	balance := get(t, api, "/wallets/"+source)
	var wallet struct {
		Balance int64 `json:"balance"`
	}
	decodeBody(t, balance, &wallet)
	if wallet.Balance != 750 {
		t.Errorf("balance = %d, want 750", wallet.Balance)
	}

	history := get(t, api, fmt.Sprintf("/wallets/%s/transfers", source))
	var list struct {
		Transfers []struct {
			Status string `json:"status"`
		} `json:"transfers"`
	}
	decodeBody(t, history, &list)
	if len(list.Transfers) != 1 {
		t.Fatalf("history has %d transfers, want 1", len(list.Transfers))
	}
	if list.Transfers[0].Status != "PROCESSED" {
		t.Errorf("status = %q, want PROCESSED", list.Transfers[0].Status)
	}
}

func TestMalformedBodyIsRejected(t *testing.T) {
	t.Parallel()

	api, _ := newAPI(t)

	request := httptest.NewRequest(http.MethodPost, "/transfers", bytes.NewBufferString(`{"amount":`))
	response := httptest.NewRecorder()
	api.ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", response.Code)
	}
}

func newAPI(t *testing.T) (http.Handler, *testsupport.Harness) {
	t.Helper()

	h := testsupport.New(t)
	log := testsupport.Logger()
	api := handler.New(
		service.NewTransferService(h.Store, log),
		service.NewWalletService(h.Store),
		log,
	)
	return api.Routes(), h
}

func post(t *testing.T, api http.Handler, path string, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()

	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("encode request: %v", err)
	}

	request := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(encoded))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	api.ServeHTTP(response, request)
	return response
}

func get(t *testing.T, api http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()

	response := httptest.NewRecorder()
	api.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
	return response
}

func decodeBody(t *testing.T, response *httptest.ResponseRecorder, target any) {
	t.Helper()

	if err := json.Unmarshal(response.Body.Bytes(), target); err != nil {
		t.Fatalf("decode response %q: %v", response.Body, err)
	}
}
