package engines

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/schemaddl"
)

// Referencing a table does not grant management of it. This read keeps native
// raw metadata and owned-object identities, while omitting executable dumps,
// synthesized declarations and sequence-state reads. The caller separately
// establishes native REFERENCES permissions and a transaction-held lock.
func openGaussReadDesignReference(ctx context.Context, c *sql.Conn, target contract.SchemaDesignTarget) (*schemaddl.OpenGaussCatalog, error) {
	var raw sql.NullString
	if err := c.QueryRowContext(ctx, openGaussDesignReferenceCatalogSQL, target.Object.Schema, target.Object.Name).Scan(&raw); err != nil {
		return nil, err
	}
	var catalog schemaddl.OpenGaussCatalog
	var platform struct{ Platform string }
	if !raw.Valid || json.Unmarshal([]byte(raw.String), &catalog) != nil || json.Unmarshal([]byte(raw.String), &platform) != nil || catalog.Database != target.Object.Catalog || catalog.Namespace != target.Object.Schema || catalog.Name != target.Object.Name || catalog.RelationOID == 0 {
		return nil, errors.New("invalid, absent or excessive native reference catalog")
	}
	catalog.NativeByteOrder = postgresNativeByteOrder(platform.Platform)
	if _, err := catalog.Fingerprint(); err != nil {
		return nil, err
	}
	return catalog.Clone(), nil
}
