package neo4j

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"

	nd "github.com/neo4j/neo4j-go-driver/v6/neo4j"
	"github.com/nyakang/nyadb/internal/db/contract"
)

const graphDetachMaxRelationships = 2000
const graphEntityLockKey = "__nyadb_entity_lock__"

// Entity mutations remain private until their shared-grid decisions and
// previews are wired. The public data saver still admits property updates only.
// Both paths share the original document, cancellation and explicit transaction.
func (l *dataLease) saveEntityEdits(ctx context.Context, id int64, db, name string, edits []contract.DataEdit) (*contract.DataEditResult, error) {
	if err := l.identity(id, db, name); err != nil {
		return nil, err
	}
	l.mu.Lock()
	revoked := l.closed || l.lost || l.writeLost
	l.mu.Unlock()
	if revoked {
		return nil, contract.ErrRevoked
	}
	prepared, err := l.prepareEntityEdits(edits)
	if err != nil {
		return nil, err
	}
	return l.savePreparedEdits(ctx, prepared)
}

func (l *dataLease) prepareEntityEdits(edits []contract.DataEdit) ([]preparedDataEdit, error) {
	if len(edits) == 0 || len(edits) > contract.DataMaxPageSize {
		return nil, ErrBounds
	}
	prepared := make([]preparedDataEdit, 0, len(edits))
	seen := make(map[string]bool)
	total := 0
	projection := l.propertyProjection()
	for _, edit := range edits {
		var p preparedDataEdit
		if edit.Kind == "update" {
			updates, err := l.prepareDataEdits([]contract.DataEdit{edit})
			if err != nil {
				return nil, err
			}
			p = updates[0]
		} else {
			edit.Original = cloneDataSnapshot(edit.Original)
			values := make(map[string]contract.DataValue, len(edit.Values))
			for key, value := range edit.Values {
				value.Bytes = bytes.Clone(value.Bytes)
				values[key] = value
			}
			edit.Values = values
			p = preparedDataEdit{edit: edit, after: map[string]Value{}, params: map[string]any{}}
			switch edit.Kind {
			case "insert":
				if l.object.Kind != "label" || edit.Original != nil {
					return nil, contract.ErrUnsupported
				}
				p.identity.Properties = cloneGraphProjection(projection)
				for field, value := range edit.Values {
					key, ok := strings.CutPrefix(field, graphPropertyPrefix)
					if !ok || !propertyIdentity(key) {
						return nil, contract.ErrUnsupported
					}
					v, parameter, err := dataPropertyValue(value)
					if err != nil {
						return nil, err
					}
					if v.Kind == "null" {
						continue // Missing property, never a stored NULL.
					}
					p.after[key], p.params[key] = v, parameter
					if !slices.ContainsFunc(p.identity.Properties, func(c graphPropertyColumn) bool { return c.Key == key }) {
						p.identity.Properties = append(p.identity.Properties, graphPropertyColumn{Key: key, Types: []string{"Neo4j native " + v.Kind}})
					}
				}
				slices.SortFunc(p.identity.Properties, func(a, b graphPropertyColumn) int { return strings.Compare(a.Key, b.Key) })
				if !graphProjectionValid(p.identity.Properties, len(graphColumns(l.object.Kind, nil))) {
					return nil, ErrBounds
				}
			case "delete", "detach_delete":
				if len(edit.Values) != 0 || edit.Kind == "detach_delete" && l.object.Kind != "label" {
					return nil, contract.ErrUnsupported
				}
				identity, err := l.validateEditSnapshot(edit.Original)
				if err != nil {
					return nil, err
				}
				p.identity = identity
			default:
				return nil, contract.ErrUnsupported
			}
		}
		if p.identity.ElementID != "" {
			if seen[p.identity.ElementID] {
				return nil, ErrInvalid
			}
			seen[p.identity.ElementID] = true
		}
		raw, err := json.Marshal(p.edit)
		if err != nil {
			return nil, ErrInvalid
		}
		total += len(raw)
		if total > contract.DataEditMaxBytes {
			return nil, ErrBounds
		}
		prepared = append(prepared, p)
	}
	return prepared, nil
}

func (l *dataLease) runEntityEdit(ctx context.Context, c *Client, tx nd.ExplicitTransaction, receipt *dataWriteReceipt, p preparedDataEdit, budget *int) (contract.DataEditOutcome, error) {
	out := contract.DataEditOutcome{Kind: p.edit.Kind, Original: p.edit.Original}
	if p.edit.Kind == "insert" {
		query := "CREATE (entity:$($name)) SET entity=$properties RETURN entity"
		r, err := l.runDataStatement(ctx, c, tx, receipt, query, map[string]any{"name": l.object.Name, "properties": p.params}, []string{"entity"})
		if err != nil {
			return out, err
		}
		entity := r.Rows[0][0]
		if _, err = l.entityID(entity); err != nil || !slices.Equal(entity.Node.Labels, []string{l.object.Name}) || !entityPropertiesEqual(entity.Node.Properties, p.after) {
			return out, ErrProtocol
		}
		if !entityCounters(r, map[string]int64{"nodes_created": 1, "labels_added": 1, "properties_set": int64(len(p.params))}) {
			return out, ErrProtocol
		}
		previews, saved, err := l.preview(entity, p.identity.Properties, budget)
		out.Saved, out.Previews = saved, previews
		if saved != nil {
			out.PreviewColumns = slices.Clone(saved.Columns)
		}
		return out, err
	}

	lock := l.match() + " AND elementId(entity)=$element_id SET entity[$lock_key]=entity[$lock_key] RETURN entity LIMIT 2"
	params := map[string]any{"name": l.object.Name, "element_id": p.identity.ElementID, "lock_key": graphEntityLockKey}
	locked, err := l.runDataWrite(ctx, c, tx, receipt, lock, params)
	if err != nil {
		return out, err
	}
	if err = l.validateLockedDataEdit(p, locked); err != nil {
		return out, err
	}
	query := l.match() + " AND elementId(entity)=$element_id DELETE entity RETURN $element_id AS element_id"
	columns := []string{"element_id"}
	wanted := map[string]int64{"nodes_deleted": 1}
	if l.object.Kind == "relationship" {
		wanted = map[string]int64{"relationships_deleted": 1}
	}
	if p.edit.Kind == "detach_delete" {
		// Holding the node's write lock serializes incident edge creation/deletion.
		// Bound the adjacency first, lock distinct edges in element-ID order, then
		// materialize their typed properties before DETACH DELETE. The bound rejects
		// the operation without deleting any entity, including self-loop nodes.
		query = l.detachDeleteQuery()
		params["edge_limit"] = int64(graphDetachMaxRelationships + 1)
		columns = append(columns, "deleted_relationships")
	}
	r, err := l.runDataStatement(ctx, c, tx, receipt, query, params, columns)
	if err != nil {
		last := receipt.Statements[len(receipt.Statements)-1].Result
		if p.edit.Kind == "detach_delete" && errors.Is(err, contract.ErrDataConflict) && last.Completed && len(last.Rows) == 0 {
			return out, ErrBounds
		}
		return out, err
	}
	if r.Rows[0][0].Kind != "string" || r.Rows[0][0].Text != p.identity.ElementID {
		return out, ErrProtocol
	}
	if p.edit.Kind == "detach_delete" {
		edges := r.Rows[0][1]
		if err = validateDeletedRelationships(p.identity.ElementID, edges); err != nil {
			return out, err
		}
		wanted["relationships_deleted"] = int64(len(edges.Values))
		// A dependent self-SET of an absent key takes the native lock but
		// reports zero property updates. Existing keys report one update.
		for _, edge := range edges.Values {
			for _, field := range edge.Properties {
				if field.Name == "properties" {
					for _, property := range field.Value.Properties {
						if property.Name == graphEntityLockKey {
							wanted["properties_set"]++
						}
					}
				}
			}
		}
	}
	if !entityCounters(r, wanted) {
		return out, ErrProtocol
	}
	return out, nil
}

func (l *dataLease) detachDeleteQuery() string {
	return l.match() + " AND elementId(entity)=$element_id CALL { WITH entity MATCH (entity)-[related]-() WITH DISTINCT related ORDER BY elementId(related) LIMIT $edge_limit RETURN collect(related) AS edges } WITH entity,edges WHERE size(edges)<$edge_limit FOREACH (edge IN edges | SET edge[$lock_key]=edge[$lock_key]) WITH entity,[edge IN edges | {element_id:elementId(edge),legacy_id:id(edge),type:type(edge),start_element_id:elementId(startNode(edge)),end_element_id:elementId(endNode(edge)),properties:properties(edge)}] AS deleted_relationships DETACH DELETE entity RETURN $element_id AS element_id,deleted_relationships"
}

func entityPropertiesEqual(properties []Property, expected map[string]Value) bool {
	if len(properties) != len(expected) {
		return false
	}
	for _, property := range properties {
		want, ok := expected[property.Name]
		if !ok || !reflect.DeepEqual(property.Value, want) {
			return false
		}
	}
	return true
}

func entityCounters(r *Response, expected map[string]int64) bool {
	for name, actual := range r.Summary.Counters {
		if actual != expected[name] {
			return false
		}
	}
	return true
}

func validateDeletedRelationships(node string, value Value) error {
	if value.Kind != "list" || len(value.Values) > graphDetachMaxRelationships {
		return ErrProtocol
	}
	previous := ""
	for _, edge := range value.Values {
		if edge.Kind != "map" || len(edge.Properties) != 6 {
			return ErrProtocol
		}
		fields := make(map[string]Value, len(edge.Properties))
		for _, property := range edge.Properties {
			fields[property.Name] = property.Value
		}
		for _, name := range []string{"element_id", "type", "start_element_id", "end_element_id"} {
			if fields[name].Kind != "string" || !validDatabase(fields[name].Text) {
				return ErrProtocol
			}
		}
		id := fields["element_id"].Text
		if id <= previous || fields["legacy_id"].Kind != "integer" || fields["properties"].Kind != "map" || fields["start_element_id"].Text != node && fields["end_element_id"].Text != node {
			return ErrProtocol
		}
		previous = id
	}
	return nil
}
