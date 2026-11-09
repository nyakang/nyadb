package neo4j

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"reflect"
	"slices"
	"strings"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/jsondocument"
)

type preparedDataEdit struct {
	edit     contract.DataEdit
	identity graphDataIdentity
	keys     []string
	after    map[string]Value
	params   map[string]any
	// Non-nil binds detached deletion to the preview's complete distinct edges.
	previewEdges *Value
}

// Editing reads retain the original fingerprints alongside complete values for
// the editor. The shared grid compares this snapshot with its captured page;
// the general read-only inspector continues to receive a values snapshot.
func (l *dataLease) editRow(ctx context.Context, id int64, db, name string, ref contract.RowRef) (*contract.DataRow, error) {
	row, err := l.row(ctx, id, db, name, ref)
	if err != nil {
		return nil, err
	}
	snapshot := cloneDataSnapshot(row.Snapshot)
	snapshot.Mode = "fingerprints"
	snapshot.Fingerprints = make([]*string, len(snapshot.Original))
	for index, cell := range snapshot.Original {
		if cell.Kind != contract.Null {
			f := fingerprint(cell)
			snapshot.Fingerprints[index] = &f
		}
	}
	snapshot.Original = nil
	row.Snapshot = snapshot
	return row, nil
}

func cloneDataSnapshot(s *contract.RowSnapshot) *contract.RowSnapshot {
	if s == nil {
		return nil
	}
	copy := *s
	copy.Ref = cloneRowRef(s.Ref)
	copy.Columns = slices.Clone(s.Columns)
	copy.Original = slices.Clone(s.Original)
	for i := range copy.Original {
		copy.Original[i] = copy.Original[i].Clone()
	}
	copy.Fingerprints = make([]*string, len(s.Fingerprints))
	for i, f := range s.Fingerprints {
		if f != nil {
			x := *f
			copy.Fingerprints[i] = &x
		}
	}
	return &copy
}

// JSON cells use the same complete typed GraphValue that the native projection
// exports. Plain JSON numbers, arrays and objects cannot imply a native kind.
func dataPropertyValue(v contract.DataValue) (Value, any, error) {
	if v.Default || len(v.Text)+len(v.Bytes) > contract.DataEditMaxBytes {
		return Value{}, nil, ErrInvalid
	}
	if v.Null || v.Kind == contract.Null {
		if v.Text != "" || len(v.Bytes) != 0 || v.Binary || v.Kind != "" && v.Kind != contract.Null {
			return Value{}, nil, ErrInvalid
		}
		return Value{Kind: "null"}, nil, nil
	}
	var value Value
	if v.Binary || v.Kind == contract.Binary {
		if v.Kind != contract.Binary || v.Text != "" {
			return Value{}, nil, ErrInvalid
		}
		value = Value{Kind: "bytes", Text: base64.StdEncoding.EncodeToString(v.Bytes)}
	} else {
		if len(v.Bytes) != 0 {
			return Value{}, nil, ErrInvalid
		}
		kind := map[contract.CellKind]string{contract.Text: "string", contract.Integer: "integer", contract.Floating: "float", contract.Boolean: "boolean"}[v.Kind]
		if kind != "" {
			value = Value{Kind: kind, Text: v.Text}
		} else if v.Kind == contract.JSON {
			if err := jsondocument.Decode([]byte(v.Text), &value, contract.DataEditMaxBytes); err != nil {
				return Value{}, nil, ErrInvalid
			}
			if !exactDataPropertyJSON([]byte(v.Text)) {
				return Value{}, nil, ErrInvalid
			}
			decoder := json.NewDecoder(strings.NewReader(v.Text))
			decoder.DisallowUnknownFields()
			if decoder.Decode(&value) != nil {
				return Value{}, nil, ErrInvalid
			}
			if _, err := decoder.Token(); err != io.EOF {
				return Value{}, nil, ErrInvalid
			}
		} else {
			return Value{}, nil, ErrInvalid
		}
	}
	parameter, err := dataPropertyParameter(value)
	if err != nil || value.Kind == "null" {
		return Value{}, nil, ErrInvalid
	}
	budget := valueBudget{limit: contract.DataEditMaxBytes}
	canonical, parameter, err := budget.capture(parameter, true, 0)
	if err != nil {
		return Value{}, nil, err
	}
	return canonical, parameter, nil
}

// encoding/json folds struct field names case-insensitively. Native typed
// values use exact exported names, so differently cased duplicates must not
// overwrite a kind/component after the duplicate-name JSON check.
func exactDataPropertyJSON(raw []byte) bool {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return false
	}
	for name, raw := range fields {
		switch name {
		case "Kind", "Text":
		case "Node", "Relationship", "Path":
			if !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
				return false
			}
		case "Values":
			var values []json.RawMessage
			if json.Unmarshal(raw, &values) != nil {
				return false
			}
			for _, value := range values {
				if !exactDataPropertyJSON(value) {
					return false
				}
			}
		case "Properties":
			var values []map[string]json.RawMessage
			if json.Unmarshal(raw, &values) != nil {
				return false
			}
			for _, value := range values {
				if len(value) != 2 || value["Name"] == nil || !exactDataPropertyJSON(value["Value"]) {
					return false
				}
			}
		default:
			return false
		}
	}
	return true
}

func dataPropertyParameter(v Value) (any, error) {
	if v.Kind == "bytes" {
		if len(v.Properties) != 0 || len(v.Values) != 0 || v.Node != nil || v.Relationship != nil || v.Path != nil {
			return nil, ErrInvalid
		}
		raw, err := base64.StdEncoding.DecodeString(v.Text)
		if err != nil || len(raw) > contract.DataEditMaxBytes || base64.StdEncoding.EncodeToString(raw) != v.Text {
			return nil, ErrInvalid
		}
		return bytes.Clone(raw), nil
	}
	parameter, _, err := propertyValue(v)
	return parameter, err
}

func (l *dataLease) validateEditSnapshot(s *contract.RowSnapshot) (graphDataIdentity, error) {
	if s == nil || s.Mode != "fingerprints" && s.Mode != "values" {
		return graphDataIdentity{}, ErrInvalid
	}
	i, err := l.decodeRef(s.Ref)
	if err != nil || s.Metadata != i.Signature {
		return graphDataIdentity{}, ErrInvalid
	}
	columns := graphColumns(l.object.Kind, i.Properties)
	if len(s.Columns) != len(columns) || s.Mode == "fingerprints" && (len(s.Fingerprints) != len(columns) || len(s.Original) != 0) || s.Mode == "values" && (len(s.Original) != len(columns) || len(s.Fingerprints) != 0) {
		return graphDataIdentity{}, ErrInvalid
	}
	for index, c := range columns {
		if s.Columns[index] != c.Name || s.Mode == "fingerprints" && s.Fingerprints[index] != nil && len(*s.Fingerprints[index]) != 64 || s.Mode == "values" && s.Original[index].Truncated {
			return graphDataIdentity{}, ErrInvalid
		}
	}
	return i, nil
}

func (l *dataLease) prepareDataEdits(edits []contract.DataEdit) ([]preparedDataEdit, error) {
	if len(edits) == 0 || len(edits) > contract.DataMaxPageSize {
		return nil, ErrBounds
	}
	prepared := make([]preparedDataEdit, len(edits))
	seen := map[string]bool{}
	total := 0
	for index, edit := range edits {
		if edit.Kind != "update" || len(edit.Values) == 0 {
			return nil, contract.ErrUnsupported
		}
		edit.Original = cloneDataSnapshot(edit.Original)
		values := make(map[string]contract.DataValue, len(edit.Values))
		for name, value := range edit.Values {
			value.Bytes = bytes.Clone(value.Bytes)
			values[name] = value
		}
		edit.Values = values
		identity, err := l.validateEditSnapshot(edit.Original)
		if err != nil || seen[identity.ElementID] {
			return nil, ErrInvalid
		}
		seen[identity.ElementID] = true
		p := preparedDataEdit{edit: edit, identity: identity, after: map[string]Value{}, params: map[string]any{}}
		for field, v := range edit.Values {
			key, ok := strings.CutPrefix(field, graphPropertyPrefix)
			if !ok || !propertyIdentity(key) || !slices.ContainsFunc(identity.Properties, func(c graphPropertyColumn) bool { return c.Key == key }) {
				return nil, contract.ErrUnsupported
			}
			value, parameter, err := dataPropertyValue(v)
			if err != nil {
				return nil, err
			}
			p.after[key], p.params[key] = value, parameter
			p.keys = append(p.keys, key)
		}
		slices.Sort(p.keys)
		raw, err := json.Marshal(edit)
		if err != nil {
			return nil, ErrInvalid
		}
		total += len(raw)
		if total > contract.DataEditMaxBytes {
			return nil, ErrBounds
		}
		prepared[index] = p
	}
	return prepared, nil
}

func (l *dataLease) validateLockedDataEdit(p preparedDataEdit, entity Value) error {
	id, err := l.entityID(entity)
	if err != nil || id != p.identity.ElementID || fingerprint(entity) != p.identity.Fingerprint {
		return contract.ErrDataConflict
	}
	cells, err := l.project(entity, p.identity.Properties)
	if err != nil {
		return err
	}
	for index, cell := range cells {
		if p.edit.Original.Mode == "values" {
			if !reflect.DeepEqual(p.edit.Original.Original[index], cell) {
				return ErrInvalid
			}
			continue
		}
		f := p.edit.Original.Fingerprints[index]
		if cell.Kind == contract.Null {
			if f != nil {
				return ErrInvalid
			}
		} else if f == nil || *f != fingerprint(cell) {
			return ErrInvalid
		}
	}
	var properties []Property
	if entity.Node != nil {
		properties = entity.Node.Properties
	} else {
		properties = entity.Relationship.Properties
	}
	for _, property := range properties {
		if after, exists := p.after[property.Name]; exists && after.Kind != "null" {
			_, oldType, beforeErr := propertyValue(property.Value)
			_, newType, afterErr := propertyValue(after)
			emptyList := property.Value.Kind == "list" && after.Kind == "list" && (len(property.Value.Values) == 0 || len(after.Values) == 0)
			if property.Value.Kind != after.Kind || beforeErr != nil || afterErr != nil || oldType != newType && !emptyList {
				if property.Value.Kind != "bytes" || after.Kind != "bytes" {
					return contract.ErrUnsupported
				}
			}
		}
	}
	return nil
}
