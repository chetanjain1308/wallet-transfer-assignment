// Package config reads the service's settings from the environment.
package config

import "os"

// DefaultDatabaseURL points at the Postgres in docker-compose.yml, so the service
// and its tests run with no environment set.
// a real deployment sets DATABASE_URL.
//
//nolint:gosec // Local development credentials for the docker-compose database;
const DefaultDatabaseURL = "postgres://wallet:wallet@localhost:5432/wallet?sslmode=disable"

// Config is the full set of settings the service needs.
type Config struct {
	DatabaseURL string
	Addr        string
}

// Load reads configuration from the environment, falling back to the local
// development defaults.
func Load() Config {
	return Config{
		DatabaseURL: lookup("DATABASE_URL", DefaultDatabaseURL),
		Addr:        lookup("ADDR", ":8080"),
	}
}

func lookup(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok && value != "" {
		return value
	}
	return fallback
}
