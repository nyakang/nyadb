package neo4j

import (
	"context"
	"errors"
	"math"
	"reflect"
	"slices"
	"strconv"
	"unicode/utf8"

	"github.com/nyakang/nyadb/internal/db/contract"
)

const nodePropertyWriteQuery = "MATCH (e) WHERE elementId(e)=$id SET e[$key]=e[$key] WITH e WHERE size(labels(e))=size($labels) AND all(label IN labels(e) WHERE label IN $labels) AND valueType(e[$key])=$type AND e[$key]=$before SET e[$key]=$after RETURN elementId(e) AS identity, valueType(e[$key]) AS kind, e[$key] AS value, e AS entity"
const relationshipPropertyWriteQuery = "MATCH (s)-[e]->(t) WHERE elementId(e)=$id SET e[$key]=e[$key] WITH s,e,t WHERE type(e)=$relationship AND elementId(s)=$source AND elementId(t)=$target AND valueType(e[$key])=$type AND e[$key]=$before SET e[$key]=$after RETURN elementId(e) AS identity, valueType(e[$key]) AS kind, e[$key] AS value, e AS entity"

func propertyScalar(v contract.GraphValue) (any, string, error) {
	if len(v.Values) != 0 || len(v.Properties) != 0 || v.Node != nil || v.Relationship != nil || v.Path != nil || !utf8.ValidString(v.Text) || len(v.Text) > 1<<20 {
		return nil, "", ErrInvalid
	}
	switch v.Kind {
	case "string":
		return v.Text, "STRING NOT NULL", nil
	case "boolean":
		if v.Text == "true" {
			return true, "BOOLEAN NOT NULL", nil
		}
		if v.Text == "false" {
			return false, "BOOLEAN NOT NULL", nil
		}
	case "integer":
		if n, e := strconv.ParseInt(v.Text, 10, 64); e == nil && strconv.FormatInt(n, 10) == v.Text {
			return n, "INTEGER NOT NULL", nil
		}
	case "float":
		if n, e := strconv.ParseFloat(v.Text, 64); e == nil && !math.IsInf(n, 0) && !math.IsNaN(n) {
			return n, "FLOAT NOT NULL", nil
		}
	}
	return nil, "", ErrInvalid
}
func propertyIdentity(s string) bool { return s != "" && len(s) <= 4096 && utf8.ValidString(s) }
func propertyWriteQuery(r contract.GraphPropertyWriteRequest) (Request, error) {
	before, kind, e := propertyScalar(r.Before)
	if e != nil {
		return Request{}, e
	}
	after, newKind, e := propertyScalar(r.After)
	if e != nil || newKind != kind || !propertyIdentity(r.Property) || !propertyIdentity(r.Target.ElementID) {
		return Request{}, ErrInvalid
	}
	params := map[string]any{"id": r.Target.ElementID, "key": r.Property, "type": kind, "before": before, "after": after}
	query := ""
	switch r.Target.Kind {
	case "node":
		if r.Target.SourceID != "" || r.Target.TargetID != "" || r.Target.RelationshipType != "" || len(r.Target.Labels) > 1000 {
			return Request{}, ErrInvalid
		}
		labels := slices.Clone(r.Target.Labels)
		slices.Sort(labels)
		for i, label := range labels {
			if !propertyIdentity(label) || i > 0 && labels[i-1] == label {
				return Request{}, ErrInvalid
			}
		}
		if labels == nil {
			labels = []string{}
		}
		params["labels"], query = labels, nodePropertyWriteQuery
	case "relationship":
		if len(r.Target.Labels) != 0 || !propertyIdentity(r.Target.SourceID) || !propertyIdentity(r.Target.TargetID) || !propertyIdentity(r.Target.RelationshipType) {
			return Request{}, ErrInvalid
		}
		params["source"], params["target"], params["relationship"] = r.Target.SourceID, r.Target.TargetID, r.Target.RelationshipType
		query = relationshipPropertyWriteQuery
	default:
		return Request{}, ErrInvalid
	}
	return Request{Query: query, Database: r.Namespace.Catalog, Mode: "write", Parameters: params, MaxRows: 2, MaxBytes: 2 << 20, FetchSize: 2, TimeoutSeconds: 30}, nil
}

// WriteGraphProperty issues one native auto-commit without retries. The
// dependent SET obtains the entity write lock before comparing labels/type and
// the selected property's typed old value. Other properties are not compared.
func (l *queryLease) WriteGraphProperty(ctx context.Context, r contract.GraphPropertyWriteRequest) (contract.GraphPropertyWriteReceipt, error) {
	r = r.Clone()
	if e := l.s.checkNamespace(r.Namespace); e != nil {
		return contract.GraphPropertyWriteReceipt{}, e
	}
	q, e := propertyWriteQuery(r)
	if e != nil {
		return contract.GraphPropertyWriteReceipt{}, e
	}
	result, e := l.ExecuteGraph(ctx, q)
	receipt := contract.GraphPropertyWriteReceipt{Query: q.Query, Result: result}
	if e != nil || !result.Completed || result.Uncertain {
		return receipt, e
	}
	if result.Truncated || !slices.Equal(result.Columns, []string{"identity", "kind", "value", "entity"}) || len(result.Rows) > 1 {
		return receipt, l.markPropertyProtocolLost()
	}
	if len(result.Rows) == 0 {
		receipt.Conflict = true
		return receipt, nil
	}
	row := result.Rows[0]
	_, kind, _ := propertyScalar(r.After)
	if len(row) != 4 || row[0].Kind != "string" || row[0].Text != r.Target.ElementID || row[1].Kind != "string" || row[1].Text != kind || row[2].Kind != r.After.Kind {
		return receipt, l.markPropertyProtocolLost()
	}
	actual, _, err := propertyScalar(row[2])
	want, _, _ := propertyScalar(r.After)
	if err != nil || actual != want {
		return receipt, l.markPropertyProtocolLost()
	}
	if !propertyReceiptEntity(row[3], r.Target, r.Property, row[2]) {
		return receipt, l.markPropertyProtocolLost()
	}
	receipt.Applied = true
	return receipt, nil
}
func (l *queryLease) markPropertyProtocolLost() error {
	l.mu.Lock()
	l.lost = true
	l.mu.Unlock()
	return errors.Join(contract.ErrSQLSessionLost, ErrProtocol)
}

var _ contract.GraphPropertyWriteLease = (*queryLease)(nil)

func propertyReceiptEntity(v contract.GraphValue, target contract.GraphPropertyTarget, key string, value contract.GraphValue) bool {
	var props []contract.GraphProperty
	switch target.Kind {
	case "node":
		if v.Kind != "node" || v.Node == nil || v.Node.ElementID != target.ElementID {
			return false
		}
		labels, want := slices.Clone(v.Node.Labels), slices.Clone(target.Labels)
		slices.Sort(labels)
		slices.Sort(want)
		if !slices.Equal(labels, want) {
			return false
		}
		props = v.Node.Properties
	case "relationship":
		if v.Kind != "relationship" || v.Relationship == nil || v.Relationship.ElementID != target.ElementID || v.Relationship.StartElementID != target.SourceID || v.Relationship.EndElementID != target.TargetID || v.Relationship.Type != target.RelationshipType {
			return false
		}
		props = v.Relationship.Properties
	default:
		return false
	}
	for _, p := range props {
		if p.Name == key {
			return reflect.DeepEqual(p.Value, value)
		}
	}
	return false
}
