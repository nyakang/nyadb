package neo4j

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"

	nd "github.com/neo4j/neo4j-go-driver/v6/neo4j"
	"github.com/nyakang/nyadb/internal/db/contract"
)

// Only one retained preview belongs to an original data lease. All fields are
// protected by owner.mu; accessors never expose the submission-owned values.
type entityDataPlan struct {
	owner    *dataLease
	closed   bool
	prepared []preparedDataEdit
	snapshot contract.DataMutationSnapshot
}

func (p *entityDataPlan) Snapshot() contract.DataMutationSnapshot {
	p.owner.mu.Lock()
	defer p.owner.mu.Unlock()
	if p.closed || p.owner.entityPlan != p || p.owner.closed {
		return contract.DataMutationSnapshot{}
	}
	return contract.CloneDataMutationSnapshot(p.snapshot)
}

func (p *entityDataPlan) releaseLocked() {
	p.closed = true
	p.prepared = nil
	p.snapshot = contract.DataMutationSnapshot{}
	if p.owner.entityPlan == p {
		p.owner.entityPlan = nil
	}
}

func (p *entityDataPlan) Close() error {
	p.owner.mu.Lock()
	defer p.owner.mu.Unlock()
	p.releaseLocked()
	return nil
}

type entityPreviewObservation struct {
	Kind, Query string
	Parameters  []Property
	Read        *Response
}

// The shared grid confirms these original native observations before handing
// back the owning plan. Read observations are live; Submit compares them under locks.
func (l *dataLease) previewEntityEdits(ctx context.Context, id int64, db, name string, edits []contract.DataEdit) (contract.DataMutationPlan, error) {
	if err := l.identity(id, db, name); err != nil {
		return nil, err
	}
	ctx, done, err := l.request(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	prepared, err := l.prepareEntityEdits(edits)
	if err != nil {
		return nil, err
	}
	l.mu.Lock()
	if l.closed || l.lost || l.writeLost {
		l.mu.Unlock()
		return nil, contract.ErrRevoked
	}
	l.previewEpoch++
	epoch := l.previewEpoch
	if l.entityPlan != nil {
		l.entityPlan.releaseLocked()
	}
	l.mu.Unlock()
	c, finish, err := l.client(ctx)
	if err != nil {
		return nil, err
	}
	defer finish()
	plan := &entityDataPlan{owner: l, prepared: prepared, snapshot: contract.DataMutationSnapshot{Object: l.object}}
	observations := make([]entityPreviewObservation, 0, len(prepared))
	for i := range prepared {
		p := &prepared[i]
		item := contract.DataMutationItem{Kind: p.edit.Kind, EntityID: p.identity.ElementID}
		observation := entityPreviewObservation{Kind: p.edit.Kind}
		if p.edit.Kind == "insert" {
			observation.Query = "CREATE (entity:$($name)) SET entity=$properties RETURN entity"
			parameters := map[string]any{"name": l.object.Name, "properties": p.params}
			if e := p.edit.GraphEndpoints; e != nil {
				observed, err := l.readEndpointResponse(ctx, c, e.Source.ElementID, e.Target.ElementID)
				if err != nil {
					return nil, err
				}
				if !reflect.DeepEqual(*observed.Rows[0][0].Node, e.Source) || !reflect.DeepEqual(*observed.Rows[0][1].Node, e.Target) {
					return nil, contract.ErrDataConflict
				}
				observation.Read = &observed
				observation.Query = relationshipCreateQuery
				parameters["source"], parameters["target"] = e.Source.ElementID, e.Target.ElementID
			}
			r, err := c.Prepare(Request{Database: l.object.Catalog, Query: observation.Query, Mode: "write", Parameters: parameters, MaxRows: 2, MaxBytes: 2 << 20})
			if err != nil {
				return nil, err
			}
			observation.Parameters = r.Parameters()
		} else {
			observation.Query = l.entityPreviewQuery()
			r, parameters, err := l.readEntityPreview(ctx, c, observation.Query, map[string]any{"element_id": p.identity.ElementID, "edge_limit": int64(graphDetachMaxRelationships + 1), "include_edges": p.edit.Kind != "update"})
			if err != nil {
				return nil, err
			}
			if len(r.Rows) != 1 || !slices.Equal(r.Columns, []string{"entity", "relationships"}) || len(r.Rows[0]) != 2 {
				return nil, contract.ErrDataConflict
			}
			if err = l.validateLockedDataEdit(*p, r.Rows[0][0]); err != nil {
				return nil, err
			}
			edges := r.Rows[0][1]
			if edges.Kind == "list" && len(edges.Values) > graphDetachMaxRelationships {
				return nil, ErrBounds
			}
			if err = validateDeletedRelationships(p.identity.ElementID, edges); err != nil {
				return nil, err
			}
			item.AffectedRelationships = len(edges.Values)
			item.Blocked = p.edit.Kind == "delete" && l.object.Kind == "label" && item.AffectedRelationships > 0
			if p.edit.Kind == "detach_delete" {
				copy := edges.Clone()
				p.previewEdges = &copy
			}
			observation.Read = &r
			observation.Parameters = parameters
		}
		plan.snapshot.Edits = append(plan.snapshot.Edits, p.edit)
		plan.snapshot.Items = append(plan.snapshot.Items, item)
		observations = append(observations, observation)
		raw, err := json.Marshal(observations)
		if err != nil || len(raw) > dataWriteReceiptBytes-contract.DataEditMaxBytes {
			return nil, ErrBounds
		}
	}
	raw, err := json.Marshal(struct {
		Object       contract.ObjectRef
		Edits        []contract.DataEdit
		Observations []entityPreviewObservation
	}{l.object, plan.snapshot.Edits, observations})
	if err != nil || len(raw) > dataWriteReceiptBytes {
		return nil, ErrBounds
	}
	plan.snapshot.NativeJSON = string(raw)
	// The public accessor and the executor must own independent edit snapshots.
	plan.snapshot = contract.CloneDataMutationSnapshot(plan.snapshot)
	l.mu.Lock()
	defer l.mu.Unlock()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if l.closed || l.lost || l.writeLost || l.previewEpoch != epoch {
		return nil, contract.ErrRevoked
	}
	l.entityPlan = plan
	return plan, nil
}

func (l *dataLease) entityPreviewQuery() string {
	query := l.match() + " AND elementId(entity)=$element_id"
	if l.object.Kind == "relationship" {
		return query + " RETURN entity,[] AS relationships LIMIT 2"
	}
	return query + " CALL { WITH entity MATCH (entity)-[related]-() WHERE $include_edges WITH DISTINCT related ORDER BY elementId(related) LIMIT $edge_limit RETURN collect(related) AS edges } RETURN entity,[edge IN edges | {element_id:elementId(edge),legacy_id:id(edge),type:type(edge),start_element_id:elementId(startNode(edge)),end_element_id:elementId(endNode(edge)),properties:properties(edge)}] AS relationships LIMIT 2"
}

func (l *dataLease) readEntityPreview(ctx context.Context, c *Client, query string, parameters map[string]any) (Response, []Property, error) {
	parameters["name"] = l.object.Name
	p, err := c.Prepare(Request{Database: l.object.Catalog, Query: query, Parameters: parameters, Mode: "read", MaxRows: 2, MaxBytes: 2 << 20, FetchSize: 2})
	if err != nil {
		return Response{}, nil, err
	}
	r, err := c.Execute(ctx, p)
	if err != nil {
		return r, p.Parameters(), err
	}
	if !r.Completed || r.Uncertain || r.Summary.Database != l.object.Catalog {
		return r, p.Parameters(), ErrProtocol
	}
	if r.Truncated {
		return r, p.Parameters(), ErrBounds
	}
	for _, count := range r.Summary.Counters {
		if count != 0 {
			return r, p.Parameters(), ErrProtocol
		}
	}
	if !l.session.Alive() {
		return r, p.Parameters(), contract.ErrRevoked
	}
	return r, p.Parameters(), ctx.Err()
}

func (l *dataLease) submitEntityPlan(ctx context.Context, id int64, db, name string, supplied contract.DataMutationPlan) (*contract.DataEditResult, error) {
	if err := l.identity(id, db, name); err != nil {
		return nil, err
	}
	p, ok := supplied.(*entityDataPlan)
	if !ok || p == nil || p.owner != l {
		return nil, ErrInvalid
	}
	return l.saveCapturedEdits(ctx, func() ([]preparedDataEdit, error) {
		l.mu.Lock()
		defer l.mu.Unlock()
		if p.closed || l.entityPlan != p || l.closed || l.lost || l.writeLost {
			return nil, contract.ErrRevoked
		}
		for _, item := range p.snapshot.Items {
			if item.Blocked {
				p.releaseLocked()
				return nil, contract.ErrDataConflict
			}
		}
		prepared := p.prepared
		p.releaseLocked()
		return prepared, nil
	})
}

func (l *dataLease) lockedPreviewEdgesQuery() string {
	return l.match() + " AND elementId(entity)=$element_id CALL { WITH entity MATCH (entity)-[related]-() WITH DISTINCT related ORDER BY elementId(related) LIMIT $edge_limit RETURN collect(related) AS edges } WITH entity,edges WHERE size(edges)<$edge_limit FOREACH (edge IN edges | SET edge[$lock_key]=edge[$lock_key]) RETURN entity,[edge IN edges | {element_id:elementId(edge),legacy_id:id(edge),type:type(edge),start_element_id:elementId(startNode(edge)),end_element_id:elementId(endNode(edge)),properties:properties(edge)}] AS relationships"
}

func (l *dataLease) runPreviewedDeletion(ctx context.Context, c *Client, tx nd.ExplicitTransaction, receipt *dataWriteReceipt, p preparedDataEdit) (contract.DataEditOutcome, error) {
	out := contract.DataEditOutcome{Kind: p.edit.Kind, Original: p.edit.Original}
	params := map[string]any{"name": l.object.Name, "element_id": p.identity.ElementID, "lock_key": graphEntityLockKey, "edge_limit": int64(graphDetachMaxRelationships + 1)}
	r, err := l.runDataStatement(ctx, c, tx, receipt, l.lockedPreviewEdgesQuery(), params, []string{"entity", "relationships"})
	if err != nil {
		return out, err
	}
	if err = l.validateLockedDataEdit(p, r.Rows[0][0]); err != nil {
		return out, err
	}
	edges := r.Rows[0][1]
	if err = validateDeletedRelationships(p.identity.ElementID, edges); err != nil || !reflect.DeepEqual(edges, *p.previewEdges) {
		return out, errors.Join(contract.ErrDataConflict, err)
	}
	ids := make([]string, 0, len(edges.Values))
	for _, edge := range edges.Values {
		ids = append(ids, graphRelationshipID(edge))
	}
	// Delete only the observed edges, then use plain node DELETE. An unobserved
	// concurrent edge either fails the set comparison or causes native rollback;
	// it can never be implicitly deleted by DETACH DELETE.
	query := l.match() + " AND elementId(entity)=$element_id CALL { WITH entity MATCH (entity)-[edge]-() WITH DISTINCT edge ORDER BY elementId(edge) LIMIT $edge_limit RETURN collect(edge) AS edges } WITH entity,edges WHERE [edge IN edges | elementId(edge)]=$edge_ids FOREACH (edge IN edges | DELETE edge) DELETE entity RETURN $element_id AS element_id"
	params["edge_ids"] = ids
	r, err = l.runDataStatement(ctx, c, tx, receipt, query, params, []string{"element_id"})
	if err != nil {
		return out, err
	}
	if r.Rows[0][0].Kind != "string" || r.Rows[0][0].Text != p.identity.ElementID || !entityCounters(r, map[string]int64{"nodes_deleted": 1, "relationships_deleted": int64(len(ids))}) {
		return out, ErrProtocol
	}
	return out, nil
}

func graphRelationshipID(edge Value) string {
	for _, field := range edge.Properties {
		if field.Name == "element_id" {
			return field.Value.Text
		}
	}
	return ""
}

var _ contract.DataMutationPlan = (*entityDataPlan)(nil)
