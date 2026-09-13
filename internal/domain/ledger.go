package domain

import (
	"time"

	"github.com/google/uuid"
)

// EntryType is the side of the ledger an entry sits on.
type EntryType string

const (
	Debit  EntryType = "DEBIT"
	Credit EntryType = "CREDIT"
)

// LedgerEntry is one side of a transfer. Entries are written in pairs and never
// amended; a correction would be a new pair.
type LedgerEntry struct {
	ID         int64
	TransferID uuid.UUID
	WalletID   string
	Type       EntryType
	Amount     int64
	CreatedAt  time.Time
}

// EntriesFor builds the debit and credit pair that records t.
func EntriesFor(t Transfer) (debit, credit LedgerEntry) {
	debit = LedgerEntry{
		TransferID: t.ID,
		WalletID:   t.FromWalletID,
		Type:       Debit,
		Amount:     t.Amount,
	}
	credit = LedgerEntry{
		TransferID: t.ID,
		WalletID:   t.ToWalletID,
		Type:       Credit,
		Amount:     t.Amount,
	}
	return debit, credit
}

// SignedAmount is the entry's effect on its wallet's balance.
func (e LedgerEntry) SignedAmount() int64 {
	if e.Type == Debit {
		return -e.Amount
	}
	return e.Amount
}
