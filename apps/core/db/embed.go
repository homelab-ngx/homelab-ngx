// Package db embeds the goose migrations so internal/store can run them.
// (go:embed cannot reach parent directories, hence this package.)
package db

import "embed"

//go:embed migrations/*.sql
var Migrations embed.FS
