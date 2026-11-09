package neo4j

import (
	"context"
	"encoding/json"
	"slices"
	"strings"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/graphimport"
)

const graphImportReceiptBytes = 64 << 20
const graphImportChunkRows = 64
const graphImportNodeQuery = "UNWIND $rows AS row CREATE (entity:$(row.labels)) SET entity=row.properties RETURN row.source AS source,entity"
const graphImportUnlabeledQuery = "UNWIND $rows AS row CREATE (entity) SET entity=row.properties RETURN row.source AS source,entity"
const graphImportRelationshipQuery = "UNWIND $rows AS row MATCH (source) WHERE elementId(source)=row.start MATCH (target) WHERE elementId(target)=row.end CREATE (source)-[entity:$(row.type)]->(target) SET entity=row.properties RETURN row.source AS source,entity"
const graphImportAnchorQuery = "UNWIND $ids AS id MATCH (entity) WHERE elementId(entity)=id RETURN entity ORDER BY elementId(entity)"
const graphImportLockQuery = "UNWIND $ids AS id MATCH (entity) WHERE elementId(entity)=id WITH entity ORDER BY elementId(entity) SET entity[$lock_key]=entity[$lock_key] RETURN entity ORDER BY elementId(entity)"

type graphImportChunk struct {
	Query string
	Rows  []any
}

// The source, canonical values, native parameter chunks and complete anchor
// observations belong to this original lease. The public snapshot is a copy.
type graphDataPlan struct {
	owner                *dataLease
	closed               bool
	snapshot             contract.GraphImportSnapshot
	nodes, relationships []graphImportChunk
}

func (p *graphDataPlan) Snapshot() contract.GraphImportSnapshot {
	p.owner.mu.Lock()
	defer p.owner.mu.Unlock()
	if p.closed || p.owner.closed || p.owner.graphPlan != p {
		return contract.GraphImportSnapshot{}
	}
	return contract.CloneGraphImportSnapshot(p.snapshot)
}
func (p *graphDataPlan) releaseLocked() {
	p.closed = true
	p.snapshot = contract.GraphImportSnapshot{}
	p.nodes = nil
	p.relationships = nil
	if p.owner.graphPlan == p {
		p.owner.graphPlan = nil
	}
}
func (p *graphDataPlan) Close() error {
	p.owner.mu.Lock()
	defer p.owner.mu.Unlock()
	p.releaseLocked()
	return nil
}

func canonicalImportProperties(props []Property) ([]Property, map[string]any, error) {
	parameters := make(map[string]any, len(props))
	for _, property := range props {
		v, err := dataPropertyParameter(property.Value)
		if err != nil {
			return nil, nil, err
		}
		parameters[property.Name] = v
	}
	budget := valueBudget{limit: graphimport.MaxEntityBytes}
	return budget.properties(parameters, true, 0)
}

// Resolve the largest permitted destination ID during preflight, so a later
// generated ID cannot make relationship parameters overflow after node writes.
func graphImportChunks(query string, rows []any, relationships bool) ([]graphImportChunk, error) {
	chunks := []graphImportChunk{}
	for len(rows) > 0 {
		count := 0
		for count < min(len(rows), graphImportChunkRows) {
			trial := rows[:count+1]
			if relationships {
				trial = make([]any, count+1)
				for i := range trial {
					row := rows[i].(map[string]any)
					copy := make(map[string]any, len(row))
					for k, v := range row {
						copy[k] = v
					}
					copy["start"], copy["end"] = strings.Repeat("x", 1024), strings.Repeat("x", 1024)
					trial[i] = copy
				}
			}
			budget := valueBudget{limit: (1 << 20) - 4096}
			if _, _, err := budget.properties(map[string]any{"rows": trial}, false, 0); err != nil {
				if count == 0 {
					return nil, err
				}
				break
			}
			count++
		}
		chunks = append(chunks, graphImportChunk{Query: query, Rows: rows[:count]})
		rows = rows[count:]
	}
	return chunks, nil
}

func (l *dataLease) prepareGraphImport(request contract.GraphImportRequest) (*graphDataPlan, error) {
	if err := graphimport.ValidateRequest(request); err != nil {
		return nil, err
	}
	request = contract.CloneGraphImportRequest(request)
	bound := map[string]bool{}
	for _, b := range request.Bindings {
		bound[b.SourceID] = true
	}
	labeled, unlabeled, relationships := []any{}, []any{}, []any{}
	for i := range request.Bundle.Nodes {
		node := &request.Bundle.Nodes[i]
		properties, parameters, err := canonicalImportProperties(node.Properties)
		if err != nil {
			return nil, err
		}
		node.Properties = properties
		slices.Sort(node.Labels)
		if bound[node.ElementID] {
			continue
		}
		row := map[string]any{"source": node.ElementID, "labels": slices.Clone(node.Labels), "properties": parameters}
		if len(node.Labels) == 0 {
			unlabeled = append(unlabeled, row)
		} else {
			labeled = append(labeled, row)
		}
	}
	for i := range request.Bundle.Relationships {
		r := &request.Bundle.Relationships[i]
		properties, parameters, err := canonicalImportProperties(r.Properties)
		if err != nil {
			return nil, err
		}
		r.Properties = properties
		relationships = append(relationships, map[string]any{"source": r.ElementID, "start": r.StartElementID, "end": r.EndElementID, "type": r.Type, "properties": parameters})
	}
	if err := graphimport.ValidateRequest(request); err != nil {
		return nil, err
	}
	p := &graphDataPlan{owner: l, snapshot: contract.GraphImportSnapshot{Object: l.object, Request: request, Nodes: len(labeled) + len(unlabeled)}}
	for _, group := range []struct {
		query string
		rows  []any
	}{{graphImportNodeQuery, labeled}, {graphImportUnlabeledQuery, unlabeled}} {
		chunks, err := graphImportChunks(group.query, group.rows, false)
		if err != nil {
			return nil, err
		}
		p.nodes = append(p.nodes, chunks...)
	}
	var err error
	p.relationships, err = graphImportChunks(graphImportRelationshipQuery, relationships, true)
	return p, err
}

func (l *dataLease) previewGraphImport(ctx context.Context, id int64, db, name string, request contract.GraphImportRequest) (contract.GraphImportPlan, error) {
	if err := l.identity(id, db, name); err != nil {
		return nil, err
	}
	ctx, done, err := l.request(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	p, err := l.prepareGraphImport(request)
	if err != nil {
		return nil, err
	}
	l.mu.Lock()
	if l.closed || l.lost || l.writeLost {
		l.mu.Unlock()
		return nil, contract.ErrRevoked
	}
	l.graphEpoch++
	epoch := l.graphEpoch
	if l.graphPlan != nil {
		l.graphPlan.releaseLocked()
	}
	l.mu.Unlock()
	c, finish, err := l.client(ctx)
	if err != nil {
		return nil, err
	}
	defer finish()
	ids := graphImportAnchorIDs(p.snapshot.Request)
	observations := []dataWriteStatement{}
	anchors := map[string]Node{}
	for start := 0; start < len(ids); start += graphImportChunkRows {
		part := ids[start:min(start+graphImportChunkRows, len(ids))]
		params := map[string]any{"ids": part}
		plan, err := c.Prepare(Request{Database: db, Query: graphImportAnchorQuery, Mode: "read", Parameters: params, MaxRows: len(part) + 1, MaxBytes: graphimport.MaxBytes, FetchSize: graphImportChunkRows})
		if err != nil {
			return nil, err
		}
		r, err := c.Execute(ctx, plan)
		observations = append(observations, dataWriteStatement{Query: graphImportAnchorQuery, Parameters: plan.Parameters(), Result: r})
		if err != nil {
			return nil, err
		}
		if err = validateGraphAnchorResponse(r, db, part); err != nil {
			return nil, err
		}
		for _, row := range r.Rows {
			n := row[0].Node.Clone()
			anchors[n.ElementID] = n
		}
		if raw, err := json.Marshal(anchors); err != nil || len(raw) > graphimport.MaxBytes {
			return nil, ErrBounds
		}
	}
	for _, binding := range p.snapshot.Request.Bindings {
		p.snapshot.Anchors = append(p.snapshot.Anchors, contract.GraphImportAnchor{SourceID: binding.SourceID, Node: anchors[binding.DestinationID].Clone()})
	}
	if raw, err := json.Marshal(p.snapshot.Anchors); err != nil || len(raw) > graphimport.MaxBytes {
		return nil, ErrBounds
	}
	// Preflight actual node requests; relationship chunks use unresolved source
	// references here and are resolved only within their owning transaction.
	for _, chunk := range append(slices.Clone(p.nodes), p.relationships...) {
		prepared, err := c.Prepare(Request{Database: db, Query: chunk.Query, Mode: "write", Parameters: map[string]any{"rows": chunk.Rows}, MaxRows: len(chunk.Rows) + 1, MaxBytes: 16 << 20})
		if err != nil {
			return nil, err
		}
		observations = append(observations, dataWriteStatement{Query: chunk.Query, Parameters: prepared.Parameters()})
	}
	raw, err := json.Marshal(struct {
		Snapshot     contract.GraphImportSnapshot
		Observations []dataWriteStatement
	}{p.snapshot, observations})
	if err != nil || len(raw) > graphImportReceiptBytes/2 {
		return nil, ErrBounds
	}
	p.snapshot.NativeJSON = string(raw)
	l.mu.Lock()
	defer l.mu.Unlock()
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	if l.closed || l.lost || l.writeLost || l.graphEpoch != epoch {
		return nil, contract.ErrRevoked
	}
	l.graphPlan = p
	return p, nil
}

func graphImportAnchorIDs(r contract.GraphImportRequest) []string {
	seen := map[string]bool{}
	for _, b := range r.Bindings {
		seen[b.DestinationID] = true
	}
	ids := make([]string, 0, len(seen))
	for id := range seen {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}

func validateGraphAnchorResponse(r Response, db string, ids []string) error {
	if !r.Completed || r.Uncertain || r.Summary.Database != db || !slices.Equal(r.Columns, []string{"entity"}) {
		return ErrProtocol
	}
	if r.Truncated {
		return ErrBounds
	}
	if len(r.Rows) != len(ids) {
		return contract.ErrDataConflict
	}
	for _, count := range r.Summary.Counters {
		if count != 0 {
			return ErrProtocol
		}
	}
	for i, row := range r.Rows {
		if len(row) != 1 || row[0].Kind != "node" || row[0].Node == nil || row[0].Node.ElementID != ids[i] {
			return ErrProtocol
		}
	}
	return nil
}

var _ contract.GraphImportPlan = (*graphDataPlan)(nil)
