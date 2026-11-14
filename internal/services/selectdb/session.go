// Package selectdb selects native SelectDB semantics and retains its distinct
// connection identity. Physical leases and bounded results are shared with the
// Doris-family transport; MySQL OLTP services are not selected.
package selectdb

import (
	"context"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/doris"
)

func OpenSession(ctx context.Context, p connections.Profile, password string) (contract.Session, error) {
	return doris.OpenSelectDBSession(ctx, p, password)
}
