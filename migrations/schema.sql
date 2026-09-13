-- Applied on startup and by the test helper. Every statement is idempotent so
-- repeated application is a no-op; a production system would use versioned
-- migrations (golang-migrate or similar) instead.

CREATE TABLE IF NOT EXISTS wallets (
    id         TEXT        PRIMARY KEY,
    currency   CHAR(3)     NOT NULL,
    balance    BIGINT      NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT wallets_balance_non_negative CHECK (balance >= 0)
);

DO $$ BEGIN
    CREATE TYPE transfer_status AS ENUM ('PENDING', 'PROCESSED', 'FAILED');
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;

DO $$ BEGIN
    CREATE TYPE ledger_entry_type AS ENUM ('DEBIT', 'CREDIT');
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;

CREATE TABLE IF NOT EXISTS transfers (
    id              UUID            PRIMARY KEY,
    idempotency_key TEXT            NOT NULL UNIQUE,
    from_wallet_id  TEXT            NOT NULL REFERENCES wallets (id),
    to_wallet_id    TEXT            NOT NULL REFERENCES wallets (id),
    amount          BIGINT          NOT NULL,
    currency        CHAR(3)         NOT NULL,
    status          transfer_status NOT NULL,
    failure_reason  TEXT,
    created_at      TIMESTAMPTZ     NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ     NOT NULL DEFAULT now(),
    CONSTRAINT transfers_amount_positive CHECK (amount > 0),
    CONSTRAINT transfers_distinct_wallets CHECK (from_wallet_id <> to_wallet_id),
    -- A reason is what distinguishes a recorded failure from a successful transfer;
    -- keeping the two in step stops a FAILED row from losing why it failed.
    CONSTRAINT transfers_reason_matches_status CHECK (
        (status = 'FAILED') = (failure_reason IS NOT NULL)
    )
);

CREATE INDEX IF NOT EXISTS transfers_from_wallet_idx
    ON transfers (from_wallet_id, created_at DESC);
CREATE INDEX IF NOT EXISTS transfers_to_wallet_idx
    ON transfers (to_wallet_id, created_at DESC);

CREATE TABLE IF NOT EXISTS ledger_entries (
    id          BIGINT            GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    transfer_id UUID              NOT NULL REFERENCES transfers (id),
    wallet_id   TEXT              NOT NULL REFERENCES wallets (id),
    type        ledger_entry_type NOT NULL,
    amount      BIGINT            NOT NULL,
    created_at  TIMESTAMPTZ       NOT NULL DEFAULT now(),
    CONSTRAINT ledger_entries_amount_positive CHECK (amount > 0),
    -- Caps a transfer at one entry per side. Both sides are written in the same
    -- transaction, so with this constraint a transfer has either zero or exactly
    -- two entries and the ledger cannot go half-posted.
    CONSTRAINT ledger_entries_one_per_side UNIQUE (transfer_id, type)
);

CREATE INDEX IF NOT EXISTS ledger_entries_wallet_idx
    ON ledger_entries (wallet_id, created_at DESC);

-- The key is claimed at the start of the transfer's own transaction and linked to
-- the transfer before that transaction commits, so transfer_id is null only
-- inside the writing transaction and never to another reader. The outcome is not
-- copied here: it is read back from the transfer the key points at.
CREATE TABLE IF NOT EXISTS idempotency_records (
    key                 TEXT        PRIMARY KEY,
    request_fingerprint TEXT        NOT NULL,
    transfer_id         UUID        REFERENCES transfers (id),
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);
