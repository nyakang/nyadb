package dpi

import (
	"context"
	"database/sql/driver"
)

// ContextTransaction keeps the original native transaction while each terminal
// request supplies its own cancellation scope. Commit/Rollback retain their
// existing driver.Tx semantics for short, atomic adapter operations.
type ContextTransaction interface {
	driver.Tx
	CommitContext(context.Context) error
	RollbackContext(context.Context) error
}
