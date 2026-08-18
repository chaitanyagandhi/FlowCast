// Package migrations embeds FlowCast's SQL migrations so the binary can bring an empty
// database up to date without shipping loose files alongside it.
//
// Migrations are forward-only and numbered: NNNN_name.sql, applied in filename order.
// There are no down migrations -- rolling a schema change back in place is rarely correct
// under load, and a corrective forward migration is easier to review.
//
// Files are immutable once committed. The runner records a checksum of each applied
// migration and refuses to continue if a file changed after the fact.
package migrations

import "embed"

// FS holds every migration in this directory.
//
//go:embed *.sql
var FS embed.FS
