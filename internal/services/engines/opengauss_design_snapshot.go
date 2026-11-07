package engines

import (
	"context"
	"database/sql"

	"github.com/nyakang/nyadb/internal/db/contract"
)

// Native table design enriches the neutral snapshot from one immutable
// openGauss catalog. Copy/drop continue using the same original identity.
func openGaussReadDesign(ctx context.Context, c *sql.Conn, target contract.SchemaDesignTarget) (contract.SchemaDesignSnapshot, error) {
	snapshot, err := openGaussReadTable(ctx, c, target)
	if err != nil {
		return snapshot, err
	}
	snapshot.Schema.Table, snapshot.Schema.DDL, err = snapshot.Schema.OpenGauss.DesignTable()
	return snapshot, err
}

func (s *Session) openGaussDesignSnapshot(ctx context.Context, target contract.SchemaDesignTarget) (contract.SchemaDesignSnapshot, error) {
	snapshot, err := s.openGaussTableSnapshot(ctx, target)
	if err != nil {
		return snapshot, err
	}
	snapshot.Schema.Table, snapshot.Schema.DDL, err = snapshot.Schema.OpenGauss.DesignTable()
	return snapshot, err
}
