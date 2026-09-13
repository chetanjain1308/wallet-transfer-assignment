package domain

import "time"

// Wallet holds a stored balance in minor units of Currency. The balance is
// authoritative: it is read and written under a row lock, and the ledger is the
// audit trail that must agree with it.
type Wallet struct {
	ID        string
	Currency  string
	Balance   int64
	CreatedAt time.Time
	UpdatedAt time.Time
}

// CanDebit reports whether the wallet covers amount.
func (w Wallet) CanDebit(amount int64) bool {
	return w.Balance >= amount
}
