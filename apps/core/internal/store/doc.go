// Package store is the daemon's SQLite runtime store: goose migrations,
// sqlc-generated queries, and the hand-written Open/Tx/maintenance layer.
package store

//go:generate sqlc generate -f ../../sqlc.yaml
