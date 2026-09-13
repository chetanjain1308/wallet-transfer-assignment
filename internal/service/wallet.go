package service

import (
	"context"

	"github.com/Robustrade/wallet-transfer-assignment/internal/domain"
)

// defaultHistoryLimit caps an unbounded history request.
const defaultHistoryLimit = 50

// WalletService serves the read side and wallet creation. Transfers are the only
// thing that move balances, so nothing here writes one.
type WalletService struct {
	store Store
}

// NewWalletService wires the service to its store.
func NewWalletService(store Store) *WalletService {
	return &WalletService{store: store}
}

// Create opens a wallet with a starting balance.
func (s *WalletService) Create(ctx context.Context, w domain.Wallet) (domain.Wallet, error) {
	if err := validateNewWallet(w); err != nil {
		return domain.Wallet{}, err
	}
	if err := s.store.CreateWallet(ctx, w); err != nil {
		return domain.Wallet{}, err
	}
	return s.store.WalletByID(ctx, w.ID)
}

// Get returns a wallet and its current balance.
func (s *WalletService) Get(ctx context.Context, id string) (domain.Wallet, error) {
	return s.store.WalletByID(ctx, id)
}

// History returns the wallet's transfers, most recent first, including attempts
// that failed.
func (s *WalletService) History(ctx context.Context, id string, limit int) ([]domain.Transfer, error) {
	if _, err := s.store.WalletByID(ctx, id); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > defaultHistoryLimit {
		limit = defaultHistoryLimit
	}
	return s.store.TransfersForWallet(ctx, id, limit)
}

func validateNewWallet(w domain.Wallet) error {
	switch {
	case w.ID == "":
		return domain.ErrMissingWalletID
	case len(w.Currency) != 3:
		return domain.ErrInvalidCurrency
	case w.Balance < 0:
		return domain.ErrNegativeBalance
	}
	return nil
}
