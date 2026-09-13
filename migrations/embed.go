// Package migrations carries the database schema so it ships inside the binary.
package migrations

import _ "embed"

// Schema is the full, idempotent DDL for the service.
//
//go:embed schema.sql
var Schema string
