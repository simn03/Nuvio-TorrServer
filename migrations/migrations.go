// Package migrations holds the embedded SQL schema applied on startup.
package migrations

import "embed"

// FS contains the ordered *.sql migration files, applied lexicographically.
//
//go:embed *.sql
var FS embed.FS
