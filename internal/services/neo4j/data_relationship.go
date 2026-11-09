package neo4j

import (
	"context"
	"crypto/hmac"
	"encoding/json"
	"reflect"
	"slices"

	nd "github.com/neo4j/neo4j-go-driver/v6/neo4j"
	"github.com/nyakang/nyadb/internal/db/contract"
)

const relationshipEndpointQuery = "MATCH (source),(target) WHERE elementId(source)=$source AND elementId(target)=$target RETURN source,target LIMIT 2"
const relationshipCreateQuery = "MATCH (source),(target) WHERE elementId(source)=$source AND elementId(target)=$target CREATE (source)-[entity:$($name)]->(target) SET entity=$properties RETURN entity"

func (l *dataLease) readGraphEndpoints(ctx context.Context, id int64, db, name, source, target string) (*contract.DataGraphEndpoints, error) {
	if err := l.identity(id, db, name); err != nil {
		return nil, err
	}
	if l.object.Kind != "relationship" {
		return nil, contract.ErrUnsupported
	}
	if !validDatabase(source) || !validDatabase(target) {
		return nil, ErrInvalid
	}
	ctx, done, err := l.request(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	c, finish, err := l.client(ctx)
	if err != nil {
		return nil, err
	}
	defer finish()
	r, err := l.readEndpointResponse(ctx, c, source, target)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(r)
	if err != nil {
		return nil, ErrInvalid
	}
	e := &contract.DataGraphEndpoints{Object: l.object, Source: r.Rows[0][0].Node.Clone(), Target: r.Rows[0][1].Node.Clone(), NativeJSON: string(raw)}
	e.Token = l.sign(graphEndpointSigningValue(*e))
	raw, err = json.Marshal(e)
	if err != nil || len(raw) > contract.DataEditMaxBytes {
		return nil, ErrBounds
	}
	l.mu.Lock()
	revoked := l.closed || l.lost || l.writeLost
	l.mu.Unlock()
	if revoked {
		return nil, contract.ErrRevoked
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return e, nil
}

func graphEndpointSigningValue(e contract.DataGraphEndpoints) any {
	e.Token = ""
	return struct {
		Purpose string
		Value   contract.DataGraphEndpoints
	}{"original-relationship-endpoints-v1", e}
}

func (l *dataLease) validateGraphEndpoints(e *contract.DataGraphEndpoints) error {
	if e == nil || l.object.Kind != "relationship" || e.Object != l.object || e.NativeJSON == "" || len(e.Token) != 64 || !validDatabase(e.Source.ElementID) || !validDatabase(e.Target.ElementID) {
		return ErrInvalid
	}
	raw, err := json.Marshal(e)
	if err != nil || len(raw) > contract.DataEditMaxBytes {
		return ErrBounds
	}
	if !hmac.Equal([]byte(e.Token), []byte(l.sign(graphEndpointSigningValue(*e)))) {
		return ErrInvalid
	}
	if e.Source.ElementID == e.Target.ElementID && !reflect.DeepEqual(e.Source, e.Target) {
		return ErrInvalid
	}
	return nil
}

func (l *dataLease) readEndpointResponse(ctx context.Context, c *Client, source, target string) (Response, error) {
	r, _, err := l.readEntityPreview(ctx, c, relationshipEndpointQuery, map[string]any{"source": source, "target": target})
	if err != nil {
		return r, err
	}
	if !slices.Equal(r.Columns, []string{"source", "target"}) {
		return r, ErrProtocol
	}
	if len(r.Rows) != 1 || len(r.Rows[0]) != 2 {
		return r, contract.ErrDataConflict
	}
	for i, id := range []string{source, target} {
		v := r.Rows[0][i]
		if v.Kind != "node" || v.Node == nil || v.Node.ElementID != id {
			return r, ErrProtocol
		}
	}
	return r, nil
}

// All unique endpoints are locked before this batch admits any relationship
// mutation. Lock order is independent of source/target direction and row order.
// A later relationship-row conflict still rolls back provisional creations.
func (l *dataLease) lockRelationshipEndpoints(ctx context.Context, c *Client, tx nd.ExplicitTransaction, receipt *dataWriteReceipt, prepared []preparedDataEdit) error {
	nodes := map[string]Node{}
	for _, p := range prepared {
		if e := p.edit.GraphEndpoints; e != nil {
			for _, node := range []Node{e.Source, e.Target} {
				if previous, exists := nodes[node.ElementID]; exists && !reflect.DeepEqual(previous, node) {
					return contract.ErrDataConflict
				}
				nodes[node.ElementID] = node
			}
		}
	}
	ids := make([]string, 0, len(nodes))
	for id := range nodes {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		query := "MATCH (entity) WHERE elementId(entity)=$element_id SET entity[$lock_key]=entity[$lock_key] RETURN entity LIMIT 2"
		v, err := l.runDataWrite(ctx, c, tx, receipt, query, map[string]any{"element_id": id, "lock_key": graphEntityLockKey})
		if err != nil {
			return err
		}
		if v.Kind != "node" || v.Node == nil || !reflect.DeepEqual(*v.Node, nodes[id]) {
			return contract.ErrDataConflict
		}
	}
	return nil
}

func (l *dataLease) validateCreatedRelationship(entity Value, p preparedDataEdit) error {
	e := p.edit.GraphEndpoints
	if _, err := l.entityID(entity); err != nil || e == nil || entity.Relationship == nil {
		return ErrProtocol
	}
	r := entity.Relationship
	if r.StartElementID != e.Source.ElementID || r.EndElementID != e.Target.ElementID || r.StartLegacyID != e.Source.LegacyID || r.EndLegacyID != e.Target.LegacyID || !entityPropertiesEqual(r.Properties, p.after) {
		return ErrProtocol
	}
	return nil
}
