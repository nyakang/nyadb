package neo4j

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"time"

	nd "github.com/neo4j/neo4j-go-driver/v6/neo4j"
	"github.com/nyakang/nyadb/internal/db/contract"
)

type graphImportReceipt struct {
	Object                                               contract.ObjectRef
	Request                                              contract.GraphImportRequest
	Anchors                                              []contract.GraphImportAnchor
	Statements                                           []dataWriteStatement
	Identities                                           []contract.GraphImportIdentity
	BeginConfirmed, CommitAttempted, CommitConfirmed     bool
	RollbackAttempted, RollbackConfirmed, OutcomeUnknown bool
	Bookmarks                                            []string
	FailureCode                                          string
	CleanupFailed                                        bool
}

func (l *dataLease) submitGraphImport(ctx context.Context, id int64, db, name string, supplied contract.GraphImportPlan) (*contract.GraphImportResult, error) {
	if err := l.identity(id, db, name); err != nil {
		return nil, err
	}
	p, ok := supplied.(*graphDataPlan)
	if !ok || p == nil || p.owner != l {
		return nil, ErrInvalid
	}
	ctx, done, err := l.request(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	l.writeMu.Lock()
	defer l.writeMu.Unlock()
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	l.mu.Lock()
	if p.closed || l.graphPlan != p || l.closed || l.lost || l.writeLost {
		l.mu.Unlock()
		return nil, contract.ErrRevoked
	}
	// Consume the single attempt before opening a client. No later invocation
	// can reuse these values, even after local validation or connection failure.
	snapshot, nodes, relationships := p.snapshot, p.nodes, p.relationships
	p.releaseLocked()
	l.mu.Unlock()
	c, finish, err := l.client(ctx)
	if err != nil {
		return nil, err
	}
	defer finish()
	return l.graphImportTransaction(ctx, c, snapshot, nodes, relationships)
}

func (l *dataLease) graphImportTransaction(parent context.Context, c *Client, snapshot contract.GraphImportSnapshot, nodes, relationships []graphImportChunk) (out *contract.GraphImportResult, err error) {
	c.operation.Lock()
	defer c.operation.Unlock()
	if !c.Alive() {
		return nil, ErrRetired
	}
	ctx, cancel := context.WithTimeout(parent, time.Duration(c.config.RequestTimeoutSeconds)*time.Second)
	defer cancel()
	stop := context.AfterFunc(c.ctx, cancel)
	defer stop()
	noRetry := true
	session := c.driver.NewSession(ctx, nd.SessionConfig{DatabaseName: l.object.Catalog, AccessMode: nd.AccessModeWrite, FetchSize: graphImportChunkRows, DisableAutoCommitRetries: &noRetry})
	receipt := graphImportReceipt{Object: l.object, Request: snapshot.Request, Anchors: snapshot.Anchors}
	out = &contract.GraphImportResult{Object: l.object}
	capture := func() ([]byte, error) { return json.Marshal(receipt) }
	defer func() {
		cleanup, finish := context.WithTimeout(context.Background(), 5*time.Second)
		defer finish()
		if session.Close(cleanup) != nil {
			receipt.CleanupFailed = true
		}
		if err != nil {
			var native *nd.Neo4jError
			if errors.As(err, &native) {
				receipt.FailureCode = native.Code
			}
		}
		out.BeginConfirmed, out.CommitAttempted, out.CommitConfirmed = receipt.BeginConfirmed, receipt.CommitAttempted, receipt.CommitConfirmed
		out.RollbackAttempted, out.RollbackConfirmed = receipt.RollbackAttempted, receipt.RollbackConfirmed
		receipt.OutcomeUnknown = out.OutcomeUnknown
		raw, marshalErr := capture()
		if marshalErr != nil || len(raw) > graphImportReceiptBytes {
			err = errors.Join(err, ErrBounds)
			out.OutcomeUnknown = true
		} else {
			out.NativeJSON = string(raw)
		}
		if out.OutcomeUnknown || err != nil {
			out.Identities = nil
			out.NodesCreated = 0
			out.RelationshipsCreated = 0
		}
		if out.OutcomeUnknown {
			l.mu.Lock()
			l.writeLost = true
			l.mu.Unlock()
		}
	}()
	if err = ctx.Err(); err != nil {
		return out, err
	}
	tx, err := session.BeginTransaction(ctx, func(config *nd.TransactionConfig) {
		config.Timeout = time.Duration(c.config.RequestTimeoutSeconds) * time.Second
	})
	if err != nil {
		return out, err
	}
	receipt.BeginConfirmed = true
	transactionFailed := false
	defer func() {
		cleanup, finish := context.WithTimeout(context.Background(), 5*time.Second)
		defer finish()
		if !receipt.CommitAttempted {
			receipt.RollbackAttempted = true
			rollbackErr := tx.Rollback(cleanup)
			receipt.RollbackConfirmed = rollbackErr == nil && !transactionFailed && ctx.Err() == nil
			if !receipt.RollbackConfirmed && len(receipt.Statements) > 0 {
				out.OutcomeUnknown = true
			}
			err = errors.Join(err, rollbackErr)
		}
		if tx.Close(cleanup) != nil {
			receipt.CleanupFailed = true
		}
	}()
	run := func(query string, params map[string]any, columns []string, rows int) (*Response, error) {
		r, runErr := l.runNativeDataStatement(ctx, c, tx, &receipt.Statements, capture, graphImportReceiptBytes, query, params, columns, rows+1, rows)
		if runErr != nil {
			transactionFailed = !localDataWriteError(runErr)
		}
		return r, runErr
	}
	anchors := map[string]Node{}
	resolved := map[string]Node{}
	createdNodeIDs, createdRelationshipIDs := map[string]bool{}, map[string]bool{}
	for _, a := range snapshot.Anchors {
		anchors[a.Node.ElementID] = a.Node
		createdNodeIDs[a.Node.ElementID] = true
		resolved[a.SourceID] = a.Node
		receipt.Identities = append(receipt.Identities, contract.GraphImportIdentity{Kind: "binding", SourceID: a.SourceID, DestinationID: a.Node.ElementID})
	}
	ids := graphImportAnchorIDs(snapshot.Request)
	for start := 0; start < len(ids); start += graphImportChunkRows {
		part := ids[start:min(start+graphImportChunkRows, len(ids))]
		r, runErr := run(graphImportLockQuery, map[string]any{"ids": part, "lock_key": graphEntityLockKey}, []string{"entity"}, len(part))
		if runErr != nil {
			return out, runErr
		}
		if r.Summary.Counters["properties_set"] < 0 || r.Summary.Counters["properties_set"] > int64(len(part)) || !entityCounters(r, map[string]int64{"properties_set": r.Summary.Counters["properties_set"]}) {
			return out, ErrProtocol
		}
		for i, row := range r.Rows {
			if row[0].Kind != "node" || row[0].Node == nil || row[0].Node.ElementID != part[i] || !reflect.DeepEqual(*row[0].Node, anchors[part[i]]) {
				return out, contract.ErrDataConflict
			}
		}
	}
	expectedNodes := map[string]Node{}
	for _, n := range snapshot.Request.Bundle.Nodes {
		expectedNodes[n.ElementID] = n
	}
	for _, chunk := range nodes {
		r, runErr := run(chunk.Query, map[string]any{"rows": chunk.Rows}, []string{"source", "entity"}, len(chunk.Rows))
		if runErr != nil {
			return out, runErr
		}
		labels, properties := 0, 0
		seen := map[string]bool{}
		for _, row := range r.Rows {
			if row[0].Kind != "string" || row[1].Kind != "node" || row[1].Node == nil {
				return out, ErrProtocol
			}
			source := row[0].Text
			node := row[1].Node
			want, exists := expectedNodes[source]
			if !exists || seen[source] || resolved[source].ElementID != "" || !importChunkContains(chunk, source) || !validImportDestinationID(node.ElementID) || createdNodeIDs[node.ElementID] {
				return out, ErrProtocol
			}
			observedLabels := slices.Clone(node.Labels)
			slices.Sort(observedLabels)
			if !slices.Equal(observedLabels, want.Labels) || !reflect.DeepEqual(node.Properties, want.Properties) {
				return out, ErrProtocol
			}
			seen[source] = true
			createdNodeIDs[node.ElementID] = true
			labels += len(want.Labels)
			properties += len(want.Properties)
			resolved[source] = node.Clone()
			receipt.Identities = append(receipt.Identities, contract.GraphImportIdentity{Kind: "node", SourceID: source, DestinationID: node.ElementID})
		}
		if !entityCounters(r, map[string]int64{"nodes_created": int64(len(chunk.Rows)), "labels_added": int64(labels), "properties_set": int64(properties)}) {
			return out, ErrProtocol
		}
	}
	expectedRelationships := map[string]Relationship{}
	for _, r := range snapshot.Request.Bundle.Relationships {
		expectedRelationships[r.ElementID] = r
	}
	for _, chunk := range relationships {
		rows := make([]any, len(chunk.Rows))
		for i, raw := range chunk.Rows {
			original := raw.(map[string]any)
			row := make(map[string]any, len(original))
			for key, value := range original {
				row[key] = value
			}
			start, end := resolved[row["start"].(string)], resolved[row["end"].(string)]
			if !validImportDestinationID(start.ElementID) || !validImportDestinationID(end.ElementID) {
				return out, ErrProtocol
			}
			row["start"], row["end"] = start.ElementID, end.ElementID
			rows[i] = row
		}
		r, runErr := run(chunk.Query, map[string]any{"rows": rows}, []string{"source", "entity"}, len(rows))
		if runErr != nil {
			return out, runErr
		}
		seen := map[string]bool{}
		properties := 0
		for _, row := range r.Rows {
			if row[0].Kind != "string" || row[1].Kind != "relationship" || row[1].Relationship == nil {
				return out, ErrProtocol
			}
			source := row[0].Text
			edge := row[1].Relationship
			want, exists := expectedRelationships[source]
			if !exists || seen[source] || !importChunkContains(chunk, source) || !validImportDestinationID(edge.ElementID) || createdRelationshipIDs[edge.ElementID] {
				return out, ErrProtocol
			}
			start, end := resolved[want.StartElementID], resolved[want.EndElementID]
			if edge.Type != want.Type || edge.StartElementID != start.ElementID || edge.EndElementID != end.ElementID || edge.StartLegacyID != start.LegacyID || edge.EndLegacyID != end.LegacyID || !reflect.DeepEqual(edge.Properties, want.Properties) {
				return out, ErrProtocol
			}
			seen[source] = true
			createdRelationshipIDs[edge.ElementID] = true
			properties += len(want.Properties)
			receipt.Identities = append(receipt.Identities, contract.GraphImportIdentity{Kind: "relationship", SourceID: source, DestinationID: edge.ElementID})
		}
		if !entityCounters(r, map[string]int64{"relationships_created": int64(len(rows)), "properties_set": int64(properties)}) {
			return out, ErrProtocol
		}
	}
	if err = ctx.Err(); err != nil {
		return out, err
	}
	receipt.CommitAttempted = true
	if err = tx.Commit(ctx); err != nil {
		out.OutcomeUnknown = true
		return out, err
	}
	receipt.CommitConfirmed = true
	receipt.Bookmarks = slices.Clone(session.LastBookmarks())
	if len(receipt.Bookmarks) == 0 {
		out.OutcomeUnknown = true
		return out, ErrProtocol
	}
	out.Identities = slices.Clone(receipt.Identities)
	out.NodesCreated = snapshot.Nodes
	out.RelationshipsCreated = len(snapshot.Request.Bundle.Relationships)
	return out, nil
}

func validImportDestinationID(id string) bool { return validDatabase(id) && len(id) <= 1024 }
func importChunkContains(chunk graphImportChunk, source string) bool {
	return slices.ContainsFunc(chunk.Rows, func(row any) bool { return row.(map[string]any)["source"] == source })
}
