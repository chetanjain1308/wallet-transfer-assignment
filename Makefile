# Override when 5432 is already in use locally: make test DB_PORT=5433
DB_PORT ?= 5432
export DB_PORT

DATABASE_URL ?= postgres://wallet:wallet@localhost:$(DB_PORT)/wallet?sslmode=disable
export DATABASE_URL
export TEST_DATABASE_URL = $(DATABASE_URL)

.PHONY: up down run test lint fmt fmt-check tidy

up:
	docker compose up -d --wait

down:
	docker compose down -v

run: up
	go run ./cmd/server

# The suite needs the database: the guarantees under test are Postgres row locks
# and unique indexes, which no in-memory substitute would exercise.
test: up
	go test ./... -race -count=1

lint:
	golangci-lint run ./...

fmt:
	gofmt -w .

fmt-check:
	test -z "$$(gofmt -l .)"

tidy:
	go mod tidy
