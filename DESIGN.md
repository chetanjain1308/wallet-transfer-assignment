# Wallet Transfer Service — Design and Submission Notes

Everything about this submission is in this document: what was built, how to run
and test it, the API it exposes, the schema, the reasoning behind the
idempotency and concurrency strategies, and the tradeoffs I accepted.

## Contents

- [Problem](#problem)
- [What was built](#what-was-built)
- [Running it](#running-it)
- [Testing it](#testing-it)
- [API](#api)
- [Database schema](#database-schema)
- [Transaction and locking strategy](#transaction-and-locking-strategy)
- [Idempotency strategy](#idempotency-strategy)
- [Failure taxonomy](#failure-taxonomy)
- [Transfer state machine](#transfer-state-machine)
- [Code structure and layering](#code-structure-and-layering)
- [Testing strategy](#testing-strategy)
- [Observability](#observability)
- [CI](#ci)
- [Assumptions and tradeoffs](#assumptions-and-tradeoffs)

## Problem

Move funds between two wallets over an HTTP API that may be called more than once
for the same intent. A duplicate delivery must not move money twice, concurrent
debits must not overdraw a wallet, and every transfer that moves money must leave
a balanced pair of ledger entries.

## What was built

A Go service over PostgreSQL. Roughly 2,550 lines across 24 files, of which about
a third is tests.

- `POST /transfers` with exactly-once semantics keyed on `idempotencyKey`
- Double-entry ledger: every processed transfer writes exactly one `DEBIT` and
  one `CREDIT`
- Stored wallet balances, moved only under a row lock
- A three-state transfer lifecycle with guarded transitions
- Supporting read endpoints: wallet balance and transfer history, plus wallet
  creation so the service can be exercised

Standard library `net/http` and `database/sql`. Two direct dependencies: `pgx`
for the Postgres driver and `google/uuid`. No web framework, no ORM, no mocking
library.

## Running it

**Requirements:** Go 1.24 and Docker (for PostgreSQL 16).

```bash
make run
```

That starts PostgreSQL, waits for its healthcheck, applies the schema and serves
on `:8080`.

If port 5432 is already taken on your machine:

```bash
make run DB_PORT=5433
```

### Configuration

| Variable | Default | Purpose |
|---|---|---|
| `DATABASE_URL` | `postgres://wallet:wallet@localhost:5432/wallet?sslmode=disable` | Database connection |
| `ADDR` | `:8080` | Listen address |
| `DB_PORT` | `5432` | Host port docker-compose publishes |
| `TEST_DATABASE_URL` | falls back to `DATABASE_URL`'s default | Database the tests use |

### Without a local Go toolchain

Everything can be driven through containers instead:

```bash
docker compose up -d

docker run --rm --network host \
  -v "$PWD":/src -v gomod:/go/pkg/mod -v gocache:/root/.cache/go-build -w /src \
  -e TEST_DATABASE_URL="postgres://wallet:wallet@localhost:5432/wallet?sslmode=disable" \
  golang:1.24 sh -c "git config --global --add safe.directory /src; go test ./... -race"
```

## Testing it

```bash
make test
```

The suite runs under `-race`. Coverage is 71.6% of statements measured with
`-coverpkg=./...`, which is the figure that matters here: the store and handler
are exercised by tests that live in other packages, and without that flag their
coverage reads as zero.

**The tests need a real PostgreSQL.** The guarantees under test — that a
duplicate request cannot double-spend, that twenty concurrent debits cannot
overdraw a wallet, that opposing transfers do not deadlock — are properties of
row locks and unique indexes. A mocked repository would let every one of these
tests pass while the deployed service lost money. `make test` brings the database
up first.

To point at a database you already have:

```bash
TEST_DATABASE_URL=postgres://user:pass@host:5432/db?sslmode=disable go test ./... -race
```

## API

### Create a transfer

```bash
curl -X POST localhost:8080/transfers -H 'Content-Type: application/json' -d '{
  "idempotencyKey": "key-1",
  "fromWalletId": "acct_a",
  "toWalletId": "acct_b",
  "amount": 400
}'
```

```json
{
  "id": "5f1570fe-00f2-45fb-80f4-a94921c9a7b0",
  "idempotencyKey": "key-1",
  "fromWalletId": "acct_a",
  "toWalletId": "acct_b",
  "amount": 400,
  "currency": "USD",
  "status": "PROCESSED",
  "createdAt": "2026-09-11T16:12:46.352768Z"
}
```

`amount` is in minor units (cents). Currency is not accepted from the caller — it
is taken from the wallets, so a request cannot assert a currency the wallets do
not hold.

Repeating the call with the same key returns that same transfer, byte for byte,
with an `Idempotent-Replay: true` header added. Repeating it with the same key
but different terms is a `409`.

| Status | When |
|---|---|
| `201` | Transfer processed, or a replay of one that was |
| `400` | Malformed body, missing key, same wallet on both sides, non-positive amount |
| `404` | Either wallet does not exist |
| `409` | The key was already used for a different request |
| `422` | Insufficient funds, or the wallets hold different currencies |

A `422` is a *recorded* refusal. The transfer exists in `FAILED`, appears in the
wallet's history, and comes back in the error body:

```json
{
  "error": { "code": "INSUFFICIENT_FUNDS", "message": "insufficient funds" },
  "transfer": {
    "id": "01e0d9b6-378b-4575-aafd-053fd93090b4",
    "status": "FAILED",
    "failureReason": "INSUFFICIENT_FUNDS",
    "amount": 99999,
    "...": "..."
  }
}
```

Replaying its key returns the same `422`. The other failure classes persist
nothing, so retrying one is evaluated afresh. Why the line falls there is in
[Failure taxonomy](#failure-taxonomy).

### Supporting endpoints

```bash
curl -X POST localhost:8080/wallets -H 'Content-Type: application/json' \
  -d '{"id":"acct_a","currency":"USD","balance":1000}'

curl localhost:8080/wallets/acct_a
curl localhost:8080/wallets/acct_a/transfers?limit=20
curl localhost:8080/healthz
```

History includes failed attempts — they are part of what happened to the wallet
even though they moved nothing.

## Database schema

Four tables.

```mermaid
erDiagram
    wallets {
        text        id PK
        char        currency
        bigint      balance "CHECK non-negative"
        timestamptz created_at
        timestamptz updated_at
    }

    transfers {
        uuid            id PK
        text            idempotency_key UK
        text            from_wallet_id FK
        text            to_wallet_id FK
        bigint          amount "CHECK positive"
        char            currency
        transfer_status status "PENDING PROCESSED FAILED"
        text            failure_reason "set iff FAILED"
        timestamptz     created_at
        timestamptz     updated_at
    }

    ledger_entries {
        bigint            id PK
        uuid              transfer_id FK "UNIQUE with type"
        text              wallet_id FK
        ledger_entry_type type "DEBIT CREDIT"
        bigint            amount "CHECK positive"
        timestamptz       created_at
    }

    idempotency_records {
        text        key PK
        text        request_fingerprint
        uuid        transfer_id FK
        timestamptz created_at
    }

    wallets   ||--o{ transfers      : "debited as source"
    wallets   ||--o{ transfers      : "credited as destination"
    wallets   ||--o{ ledger_entries : "posted to"
    transfers ||--o{ ledger_entries : "exactly 2 when PROCESSED, 0 when FAILED"
    transfers ||--|| idempotency_records : "claimed by exactly one key"
```

The two constraints doing the real work are not visible as columns:
`UNIQUE (transfer_id, type)` on `ledger_entries`, which is what makes "exactly
two entries" true rather than merely intended, and `UNIQUE` on
`idempotency_records.key`, which is the deduplication primitive itself.

Below is the whole of `migrations/schema.sql`, reproduced so this document stands
alone.

```sql
CREATE TABLE IF NOT EXISTS wallets (
    id         TEXT        PRIMARY KEY,
    currency   CHAR(3)     NOT NULL,
    balance    BIGINT      NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT wallets_balance_non_negative CHECK (balance >= 0)
);

CREATE TYPE transfer_status   AS ENUM ('PENDING', 'PROCESSED', 'FAILED');
CREATE TYPE ledger_entry_type AS ENUM ('DEBIT', 'CREDIT');

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
    CONSTRAINT transfers_amount_positive   CHECK (amount > 0),
    CONSTRAINT transfers_distinct_wallets  CHECK (from_wallet_id <> to_wallet_id),
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
    CONSTRAINT ledger_entries_one_per_side UNIQUE (transfer_id, type)
);

CREATE INDEX IF NOT EXISTS ledger_entries_wallet_idx
    ON ledger_entries (wallet_id, created_at DESC);

CREATE TABLE IF NOT EXISTS idempotency_records (
    key                 TEXT        PRIMARY KEY,
    request_fingerprint TEXT        NOT NULL,
    transfer_id         UUID        REFERENCES transfers (id),
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

The enum creation is wrapped in `DO $$ ... EXCEPTION WHEN duplicate_object`
blocks in the real file, since PostgreSQL has no `CREATE TYPE IF NOT EXISTS`.
Every statement is idempotent, so the schema can be applied on every start.

### wallets

Balance is **stored, not derived**. A transfer needs the current balance to
decide whether it can proceed, and summing a wallet's ledger on every request
makes the hot path grow with history. The ledger remains the audit trail, and the
tests assert the two agree.

`CHECK (balance >= 0)` is the database's own backstop, and it is not decoration —
see [Proving the lock matters](#proving-the-lock-matters), where it is what
catches a double-spend after the application-level check has been defeated.

### transfers

One row per attempt. `idempotency_key` is `UNIQUE`, so even setting the
idempotency table aside the database could not hold two transfers for one key.

`transfers_reason_matches_status` keeps status and reason in step: a `FAILED` row
cannot lose why it failed, and a `PROCESSED` row cannot carry a stale reason.

`transfers_distinct_wallets` makes a self-transfer unrepresentable rather than
merely rejected in code.

Both foreign keys to `wallets` matter more than they look — they are what decide
the [failure taxonomy](#failure-taxonomy).

### ledger_entries

Two rows per processed transfer, foreign-keyed to both the transfer and the
wallet, so an entry cannot exist without either.

`UNIQUE (transfer_id, type)` caps a transfer at one `DEBIT` and one `CREDIT`.
Both are written in the same transaction, so with this constraint a transfer has
either zero entries or exactly two — the ledger cannot end up half-posted.

Entries are never amended. A correction would be a new pair.

### idempotency_records

The key, a fingerprint of the request it was first used for, and the transfer it
produced.

**The outcome is deliberately not copied here.** An earlier draft stored the
rendered HTTP response alongside the key — which is what Stripe does, and which
guarantees a byte-identical replay even if the response format later changes. I
chose against it because it duplicates state that already lives on the transfer
row, and duplicated state drifts. Instead the record points at the transfer, and
a replay reads the outcome back from it. What makes this safe is that the
error-to-HTTP mapping is a pure function of the error, so the same transfer
always renders the same response.

The tradeoff is real: change that mapping and old requests replay under the new
one. For a service whose response shape is a published contract, storing the
response is the safer choice. At this size, the normalised version is the one I
can defend.

`transfer_id` is nullable because the key is claimed before the transfer exists.
It is null only for the instant inside the writing transaction, which no other
reader can observe.

## Transaction and locking strategy

### How a transfer executes

```mermaid
sequenceDiagram
    autonumber
    participant C as Caller
    participant H as Handler
    participant S as TransferService
    participant DB as PostgreSQL

    C->>H: POST /transfers
    H->>S: Transfer(req)
    S->>DB: BEGIN (READ COMMITTED)
    S->>DB: INSERT idempotency_records ON CONFLICT DO NOTHING

    alt 0 rows — key already claimed
        Note over S,DB: a concurrent duplicate blocked here<br/>until the winner committed
        S->>DB: SELECT the record
        alt fingerprint differs
            S->>DB: ROLLBACK
            H-->>C: 409 IDEMPOTENCY_KEY_REUSE
        else fingerprint matches
            S->>DB: SELECT transfer the key points at
            S->>DB: COMMIT
            H-->>C: original status + Idempotent-Replay: true
        end

    else 1 row — this caller owns the request
        S->>DB: SELECT wallets WHERE id IN (a,b) ORDER BY id FOR UPDATE
        Note over S,DB: fixed lock order — opposing<br/>transfers cannot deadlock
        S->>DB: INSERT transfer (PENDING)
        S->>DB: UPDATE idempotency_records SET transfer_id

        alt insufficient funds or currency mismatch
            S->>DB: UPDATE transfer PENDING -> FAILED
            S->>DB: COMMIT
            H-->>C: 422 + the FAILED transfer
        else funds available
            S->>DB: INSERT 2 ledger_entries (DEBIT, CREDIT)
            S->>DB: UPDATE both balances
            S->>DB: UPDATE transfer PENDING -> PROCESSED
            S->>DB: COMMIT
            H-->>C: 201 + the PROCESSED transfer
        end
    end
```

Everything between `BEGIN` and `COMMIT` is one transaction. The only path that
rolls back is key reuse, which by definition has nothing to persist.

**One transaction covers the whole transfer**: claim the key, lock both wallets,
write the transfer, write both ledger entries, move both balances, settle the
status. There is no window in which a transfer exists without its ledger entries,
or a ledger entry without its idempotency record.

Wallets are locked pessimistically, before any balance is read:

```sql
SELECT id, currency, balance, created_at, updated_at
FROM wallets
WHERE id IN ($1, $2)
ORDER BY id
FOR UPDATE
```

**Pessimistic rather than optimistic** because contention on a hot wallet is the
expected case here, not an exception. Under optimistic locking every concurrent
debit on a popular wallet fails its version check and retries, and the retry loop
becomes the thing you have to reason about — including how many retries before
giving up, and what the caller sees then. `FOR UPDATE` makes the second transfer
wait a few milliseconds instead.

**`ORDER BY id` is the deadlock guard.** Two opposing transfers — A to B and B to
A — would otherwise take the same two locks in opposite orders, and PostgreSQL
would abort one of them. Locking in a fixed order means both take A first.
`TestOpposingTransfersDoNotDeadlock` runs fifty of these against each other.

Both wallets are locked, not just the source. The credit side is a write too, and
locking one side only would leave the destination's balance racing.

### Isolation level

`READ COMMITTED`, and this one is load-bearing rather than a default left
unexamined.

Claiming a key is `INSERT ... ON CONFLICT DO NOTHING`. A concurrent duplicate
blocks on that insert until the first transaction commits, then gets zero rows
affected, and then reads the row the winner just committed. That read only sees
the winner's row because `READ COMMITTED` takes a fresh snapshot per statement.

Under `REPEATABLE READ` the same insert raises a serialization failure instead,
and every duplicate would have to be retried by the caller to get its answer.

## Idempotency strategy

**The unique index on `idempotency_records.key` is the deduplication
primitive.** Nothing is decided by reading first and then writing, which is the
pattern that races.

1. `INSERT ... ON CONFLICT DO NOTHING`. One row affected means this caller owns
   the request; zero means someone else already did.
2. The winner locks the wallets, does the work, and links the key to the transfer
   it created — all before committing.
3. A loser reads the record, compares fingerprints, and returns the transfer the
   key points at, with `Idempotent-Replay: true`.

Because the key row and the transfer commit together, a committed key always has
a transfer to point at.

The fingerprint is a SHA-256 over source, destination and amount.

### The cases that matter

**The first request commits and the response is lost.** The caller retries, takes
path 3, and gets the original transfer with the original status code. Nothing
moves twice. `TestReplayReturnsTheSameResponse` asserts this at the HTTP layer,
comparing status, headers and body.

**Same key, different payload.** The fingerprint does not match, so the request is
refused with `409` rather than being served the earlier transfer. Returning the
original would silently confirm a transfer the caller did not ask for.

**Crash between claim and commit.** The transaction is lost and the key row rolls
back with it, freeing the key. A retry executes normally. There is no state that
can be left half-applied, because there is only one transaction.

**Two deliveries arriving simultaneously.** The second blocks on the unique index
until the first commits, then replays it.
`TestConcurrentDuplicatesProduceOneTransfer` fires twenty at once and asserts
exactly one executed and all twenty got the same transfer.

**Across process restarts, and across instances.** All state is in the database;
nothing is held in memory. Two instances behave exactly like two concurrent
requests against one.

## Failure taxonomy

Where the line falls between "rejected" and "recorded" is **set by the schema,
not by preference**. A `FAILED` transfer has foreign keys to both wallets, so a
request naming a wallet that does not exist *cannot* be recorded as one.

That gives a rule that is easy to state and easy to defend:

| | Rejected | Recorded |
|---|---|---|
| **Persisted** | Nothing | Transfer committed as `FAILED` |
| **Discovered** | From the request alone | Against account state, after both wallets are loaded |
| **Cases** | Malformed body, missing key, same wallet both sides, non-positive amount, unknown wallet | Insufficient funds, currency mismatch |
| **On retry** | Evaluated afresh | Replays the same refusal |

Recorded failures belong in the wallet's history: someone tried to move money and
was refused, and that is a fact about the account worth keeping. Rejections are
facts about a malformed request, and keeping them would be logging, not ledgering.

## Transfer state machine

```
PENDING ──→ PROCESSED   (both terminal)
        └─→ FAILED
```

Transitions are guarded in two places. `domain.Transfer` refuses an illegal
transition in memory, and the SQL carries the guard too:

```sql
UPDATE transfers
SET status = $2, failure_reason = $3, updated_at = now()
WHERE id = $1 AND status = $4
RETURNING updated_at
```

So the **write itself** is what settles a transfer, rather than the status the
caller last read. Zero rows updated becomes `ErrInvalidStateTransition`.

`PROCESSED` and `FAILED` admit no transitions at all. That is what makes a
duplicate safe even if it somehow got past the idempotency check: a settled
transfer cannot be moved.

**`PENDING` is never externally observable.** The transfer is inserted as
`PENDING` and transitioned before the same transaction commits, so no reader ever
sees one. I kept it rather than inserting directly in a terminal state for two
reasons: it makes the guarded transition the mechanism that settles a transfer,
and a real system grows an asynchronous settlement step — an outbox, a rail that
acknowledges later — which needs a state to sit in. Dropping `PENDING` would be
simpler today and wrong the first time settlement stops being synchronous.

## Code structure and layering

```
cmd/server            entrypoint, wiring, graceful shutdown
internal/domain       entities, state machine, validation — no I/O
internal/service      transfer workflow, idempotency, and the ports it needs
internal/postgres     the store: SQL, locking, transaction boundaries
internal/handler      HTTP transport, error mapping, middleware
internal/testsupport  test harness against a real database
migrations            schema, embedded in the binary
```

```
handler  →  service  →  domain
                ↑
            postgres
```

`internal/service/ports.go` defines the two interfaces the service needs —
`Store` (which owns `Atomic`) and `Tx` (the writes that make up a transfer).
`internal/postgres` implements them. **The interfaces live with the consumer**, so
the service depends on nothing concrete and the dependency arrow points inward.

Two interfaces, not a tower of them. `Store` exposes `Atomic(ctx, fn)`; `Tx` is
what `fn` receives.

**Handlers** decode, call one service method, map the error to a status code.
They hold no business rules.

**The repository holds no workflow decisions.** It does not know that a transfer
has two ledger entries, or when a status may change; it is told. The one
judgement it does make is `ORDER BY id` inside `LockWallets`, which is a
persistence concern and belongs there.

**The service returns two kinds of outcome.** A rejection comes back as an error
with a zero result; a recorded failure comes back as a populated `Result` whose
`Failure` field is set, because the transfer was committed and the handler needs
to return it. Keeping them apart in the type is what lets the handler render both
correctly without re-deriving which happened.

## Testing strategy

Tests are **behavioural**. They go through the service or the HTTP handler and
assert on balances, ledger sums and status codes — never on which methods were
called. There is no mocking library and no generated mocks.

Everything but the domain unit tests runs against real PostgreSQL, for the reason
given in [Testing it](#testing-it).

| Behaviour | Test |
|---|---|
| Funds move, two entries written, ledger nets to zero | `TestTransferMovesFundsAndRecordsBothSides` |
| A duplicate returns the original and moves nothing | `TestReplayReturnsTheOriginalTransfer` |
| Same key, different terms is refused | `TestReusingAKeyWithDifferentTermsIsRefused` |
| A refusal is recorded and replays identically | `TestInsufficientFundsIsRecordedAndReplayable` |
| Currency mismatch is recorded, moves nothing | `TestMismatchedCurrenciesAreRecordedAsFailed` |
| An unknown wallet persists nothing | `TestUnknownWalletIsRejectedWithoutPersisting` |
| 20 concurrent debits against funds for 10 | `TestConcurrentDebitsCannotOverdraw` |
| 20 concurrent deliveries of one key | `TestConcurrentDuplicatesProduceOneTransfer` |
| 50 opposing transfers do not deadlock | `TestOpposingTransfersDoNotDeadlock` |
| Replay returns an identical HTTP response | `TestReplayReturnsTheSameResponse` |
| Every error maps to the right status and code | `TestErrorMapping` |
| A settled transfer cannot transition again | `TestSettledTransferCannotTransitionAgain` |
| Request validation, case by case | `TestRequestValidation` |

Tests name their own wallets with unique ids rather than truncating tables, so
cases and packages run in parallel against one database.

### Proving the lock matters

A concurrency test that passes proves nothing on its own — it may simply never
have interleaved. So I removed `FOR UPDATE` from `LockWallets` and reran:

```
--- FAIL: TestConcurrentDebitsCannotOverdraw
    Transfer() = adjust balance of "wallet_e58a8595...": ERROR: new row for
    relation "wallets" violates check constraint "wallets_balance_non_negative"
    (SQLSTATE 23514)
```

Two things fall out of that. **The test has teeth**: without the lock, concurrent
debits interleave between the balance check and the balance write, and more get
through than the wallet can fund. And **the `balance >= 0` constraint earns its
place** — it caught the overdraw that the application logic had just let past.
That is the argument for keeping correctness in the database and not only in the
service.

### Two bugs the tests caught during development

`TestReplayReturnsTheSameResponse` failed on first run: the insert never read
back the database-generated timestamps, so the original response carried a zero
`createdAt` while the replay carried the real one. Not byte-identical — exactly
what that test exists to catch. The writes now use `RETURNING`.

Running the suite in parallel surfaced a race in my own migration: two
connections issuing `CREATE TYPE` at once raise `unique_violation` on the catalog,
which the `duplicate_object` handler does not catch. Migration now runs inside a
transaction holding a PostgreSQL advisory lock, which also covers two instances
booting together in a rolling deploy.

## Observability

Structured JSON logs via `log/slog`.

Every request carries a request id — taken from `X-Request-Id` or generated —
echoed back in the response header and attached to the request context, so the
individual deliveries of a retry storm can be told apart in the log.

Every transfer decision logs its outcome, keyed by idempotency key and transfer
id: `transfer processed`, `transfer failed` with its reason, or
`transfer replayed` with the status it replayed.

An access log line per request carries method, path, status and duration. Panics
are recovered, logged, and returned as a `500` rather than killing the
connection. `GET /healthz` is a liveness check.

What a production deployment would add: counters for transfers by outcome, replay
rate and lock wait time; tracing across the transaction boundary; and an alert on
replay rate, which is the signal that a caller is retrying more than it should.

## CI

`.github/workflows/ci.yml` reads three repository variables. For this submission:

- `LINT_CMD` = `golangci-lint run ./...`
- `FORMAT_CHECK_CMD` = `test -z "$(gofmt -l .)"`
- `TEST_CMD` = `go test ./... -race -coverpkg=./... -coverprofile=coverage.out`

`-coverpkg=./...` matters: the store and handler are covered by tests in other
packages, and without it their coverage reads as zero.

`.golangci.yml` enables `errcheck`, `errorlint`, `gocritic`, `gosec`, `govet`,
`ineffassign`, `misspell`, `revive`, `staticcheck` and `unused`. The tree is
clean under all of them, and `gofmt` reports nothing.

`sonar-project.properties` has been filled in for Go — sources, test inclusions,
and the coverage report path.

Two notes on the workflow itself, which I have left as they are rather than
changing the template's CI:

- `TEST_CMD` needs a PostgreSQL reachable at `TEST_DATABASE_URL`; the workflow
  does not currently start one.
- Both jobs target a self-hosted runner label (`actions_runner_dev_new`), and the
  SonarQube job needs secrets. Neither is available to a pull request from a
  fork, so the checks will not go green there regardless of the code.

## Assumptions and tradeoffs

- **Amounts are integer minor units.** No floating point anywhere. Cross-currency
  transfers are refused rather than converted; an FX rate at a point in time is a
  separate concern with its own audit requirements.
- **No authentication.** Out of scope for the assignment. With it, wallet
  ownership would need checking before a debit, and that check belongs in the
  service, above the store.
- **Idempotency keys are global**, not scoped per caller. With authentication
  they should be scoped, so two clients cannot collide on `"1"`.
- **Idempotency records are kept forever.** A production system expires them on a
  window — 24 hours is typical — and a retry after expiry would execute again.
  That is a deliberate tradeoff between storage and the retry window you promise.
- **The schema is applied at startup** from an embedded, idempotent file behind an
  advisory lock. Real migrations want versioning and a rollback path;
  `golang-migrate` would replace this.
- **Transfer history is capped at 50** with no pagination cursor.
- **Wallet creation is unauthenticated and takes an opening balance.** That is a
  convenience for exercising the service. Real wallets open at zero and are
  funded by a deposit that posts its own ledger entries — which is also why the
  ledger currently does not balance to zero globally, only per transfer.
- **`POST /transfers` holds row locks for the duration of the request.** At high
  contention on one wallet this serialises throughput on that wallet. That is the
  correct tradeoff for money; a system needing more would batch or shard the hot
  account, not weaken the lock.
