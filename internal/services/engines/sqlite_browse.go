package engines

import (
	"context"
	"github.com/nyakang/nyadb/internal/db/contract"
)

// One catalog read obtains table flags and root pages, including autoindexes.
// It does not read structures, row contents or per-table counts.
func (s *Session) sqliteBrowse(ctx context.Context, n contract.NamespaceRef) ([]contract.DBObject, error) {
	ctx, done := s.requestContext(ctx)
	defer done()
	schema := quote(n.Schema) + ".sqlite_schema"
	rows, err := s.db.QueryContext(ctx, "SELECT t.name,t.type,t.rootpage,EXISTS(SELECT 1 FROM "+schema+" i WHERE i.type='index' AND i.tbl_name=t.name),EXISTS(SELECT 1 FROM "+schema+" g WHERE g.type='trigger' AND g.tbl_name=t.name) FROM "+schema+" t WHERE t.type IN ('table','view') AND substr(t.name,1,7)!='sqlite_' ORDER BY t.name")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []contract.DBObject{}
	for rows.Next() {
		var name, typ string
		var root int64
		var indexes, triggers bool
		if err = rows.Scan(&name, &typ, &root, &indexes, &triggers); err != nil {
			return nil, err
		}
		kind := "表"
		if typ == "view" {
			kind = "视图"
		}
		out = append(out, contract.DBObject{Ref: n.Object(typ, name), Name: name, Kind: kind, HasIndexes: &indexes, HasTriggers: &triggers, RootPage: &root})
	}
	return out, rows.Err()
}
