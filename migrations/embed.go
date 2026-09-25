// Package migrations embeds the SQL migration files so the runner can apply
// them without depending on the working directory.
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
