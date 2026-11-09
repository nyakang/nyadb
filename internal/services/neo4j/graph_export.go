package neo4j

import (
	"context"
	"encoding/json"
	"slices"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/graphimport"
)

// Exchange scans the entire original database, including unlabeled nodes and
// unrelated relationship types. It is a bounded live scan, not an atomic backup.
// A file is published only after validating complete entities and closure.
func (l *dataLease) exportGraph(ctx context.Context, object contract.ObjectRef) (contract.GraphImportBundle, error) {
	if object != l.object {
		return contract.GraphImportBundle{}, ErrInvalid
	}
	ctx, done, err := l.request(ctx)
	if err != nil {
		return contract.GraphImportBundle{}, err
	}
	defer done()
	c, finish, err := l.client(ctx)
	if err != nil {
		return contract.GraphImportBundle{}, err
	}
	defer finish()
	b := contract.GraphImportBundle{Format: graphimport.Format, Source: l.object.Namespace()}
	for _, kind := range []string{"node", "relationship"} {
		after := ""
		for {
			query := "MATCH (entity) WHERE elementId(entity)>$after RETURN entity ORDER BY elementId(entity) LIMIT 256"
			if kind == "relationship" {
				query = "MATCH ()-[entity]->() WHERE elementId(entity)>$after RETURN entity ORDER BY elementId(entity) LIMIT 256"
			}
			r, err := l.read(ctx, c, query, map[string]any{"after": after}, 257)
			if err != nil {
				return contract.GraphImportBundle{}, err
			}
			if !slices.Equal(r.Columns, []string{"entity"}) || len(r.Rows) > 256 {
				return contract.GraphImportBundle{}, ErrProtocol
			}
			for _, row := range r.Rows {
				if len(row) != 1 || row[0].Kind != kind {
					return contract.GraphImportBundle{}, ErrProtocol
				}
				var id string
				if kind == "node" {
					if row[0].Node == nil {
						return contract.GraphImportBundle{}, ErrProtocol
					}
					n := row[0].Node.Clone()
					id = n.ElementID
					b.Nodes = append(b.Nodes, n)
				} else {
					if row[0].Relationship == nil {
						return contract.GraphImportBundle{}, ErrProtocol
					}
					r := row[0].Relationship.Clone()
					id = r.ElementID
					b.Relationships = append(b.Relationships, r)
				}
				if id <= after {
					return contract.GraphImportBundle{}, ErrProtocol
				}
				after = id
			}
			if len(b.Nodes)+len(b.Relationships) > graphimport.MaxEntities {
				return contract.GraphImportBundle{}, ErrBounds
			}
			if raw, err := json.Marshal(b); err != nil || len(raw) > graphimport.MaxBytes {
				return contract.GraphImportBundle{}, ErrBounds
			}
			if len(r.Rows) < 256 {
				break
			}
		}
	}
	if err = graphimport.Validate(b); err != nil {
		return contract.GraphImportBundle{}, err
	}
	if len(graphimport.ExternalEndpoints(b)) != 0 {
		return contract.GraphImportBundle{}, contract.ErrDataConflict
	}
	if err = ctx.Err(); err != nil {
		return contract.GraphImportBundle{}, err
	}
	l.mu.Lock()
	revoked := l.closed || l.lost
	l.mu.Unlock()
	if revoked || !l.session.Alive() {
		return contract.GraphImportBundle{}, contract.ErrRevoked
	}
	return b, nil
}
