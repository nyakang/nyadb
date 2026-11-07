package schemaddl

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Incoming evidence never grants management or SELECT on the child. It binds
// the complete raw child catalog to the original parent's independently read
// FK records and the exact requested design.
type OpenGaussIncomingBaseline struct {
	original, desired, revision string
	child                       *OpenGaussCatalog
	constraints                 []OpenGaussIncomingConstraint
}

type OpenGaussIncomingConstraint struct {
	OID                               uint64
	Name                              string
	Columns, ParentColumns, ParentIDs []string
	ColumnNumbers                     []int
	Match, Update, Delete             string
	Validated, Deferrable, Deferred   bool
}

func (c OpenGaussIncomingConstraint) clone() OpenGaussIncomingConstraint {
	c.Columns = append([]string{}, c.Columns...)
	c.ParentColumns = append([]string{}, c.ParentColumns...)
	c.ParentIDs = append([]string{}, c.ParentIDs...)
	c.ColumnNumbers = append([]int{}, c.ColumnNumbers...)
	return c
}

func (b OpenGaussIncomingBaseline) Constraints() []OpenGaussIncomingConstraint {
	out := make([]OpenGaussIncomingConstraint, len(b.constraints))
	for i, constraint := range b.constraints {
		out[i] = constraint.clone()
	}
	return out
}

func (b OpenGaussIncomingBaseline) Catalog() *OpenGaussCatalog { return b.child.Clone() }

// Enumerate OIDs from native incoming records, never from navigation names or
// SQL text. Self references belong to the parent's own declaration proof.
func OpenGaussIncomingRelations(original Snapshot) ([]uint64, error) {
	if original.Engine != "opengauss" || original.OpenGauss == nil {
		return nil, ErrConflict
	}
	seen := map[uint64]bool{}
	for _, entry := range original.OpenGauss.Entries {
		if entry.Kind != "incoming_constraint" {
			continue
		}
		row, err := gaussJSON(entry.Data)
		if err != nil {
			return nil, err
		}
		child, e1 := gaussNumber(row["conrelid"])
		parent, e2 := gaussNumber(row["confrelid"])
		if e1 != nil || e2 != nil || child == 0 || parent != original.OpenGauss.RelationOID || child == parent || row["contype"] != "f" {
			return nil, ErrConflict
		}
		seen[child] = true
	}
	out := make([]uint64, 0, len(seen))
	for oid := range seen {
		out = append(out, oid)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out, nil
}

func CaptureOpenGaussIncomingBaseline(original Snapshot, desired Table, child *OpenGaussCatalog) (out OpenGaussIncomingBaseline, err error) {
	parent := original.OpenGauss
	if original.Engine != "opengauss" || !original.Exists || parent == nil || child == nil || child.RelationOID == 0 || child.RelationOID == parent.RelationOID {
		return out, ErrConflict
	}
	if _, err = PlanOpenGauss(original, desired, parent.Namespace, true); err != nil {
		return out, err
	}
	if child.Database != parent.Database || child.DatabaseOID != parent.DatabaseOID || child.RoleOID != parent.RoleOID || child.NativeByteOrder != parent.NativeByteOrder || Signature(child.Classes) != Signature(parent.Classes) || child.Hooks || parent.Hooks || PostgreSQLIdentifier(child.Name) != nil || PostgreSQLIdentifier(child.Namespace) != nil || child.NamespaceOID == 0 || (child.Namespace == parent.Namespace) != (child.NamespaceOID == parent.NamespaceOID) {
		return out, ErrConflict
	}
	revision, err := child.Fingerprint()
	if err != nil {
		return out, err
	}
	columns := map[int]string{}
	constraints := map[uint64]map[string]any{}
	main := false
	for _, entry := range child.Entries {
		if entry.Kind == "copy_definition" || entry.Kind == "design_definition" {
			return out, errors.New("native incoming evidence must omit synthesized declarations and sequence states")
		}
		row, err := gaussJSON(entry.Data)
		if err != nil {
			return out, err
		}
		if entry.Kind == "relation" && entry.OID == child.RelationOID {
			if row["relname"] != child.Name || row["relkind"] != "r" || row["relpersistence"] != "p" && row["relpersistence"] != "u" {
				return out, ErrUnsupported
			}
			main = true
		}
		if entry.Kind == "column" && entry.OID == child.RelationOID && row["attisdropped"] == false {
			name, ok := row["attname"].(string)
			owner, e1 := gaussNumber(row["attrelid"])
			number, e2 := gaussNumber(row["attnum"])
			if !ok || PostgreSQLIdentifier(name) != nil || e1 != nil || e2 != nil || owner != child.RelationOID || number == 0 || number != uint64(entry.SubID) || columns[entry.SubID] != "" {
				return out, ErrConflict
			}
			columns[entry.SubID] = name
		}
		if entry.Kind == "constraint" {
			constraints[entry.OID] = row
		}
	}
	if !main {
		return out, ErrConflict
	}
	selected := map[uint64]bool{}
	for _, entry := range parent.Entries {
		if entry.Kind != "incoming_constraint" {
			continue
		}
		var fk struct {
			OID        uint64 `json:"oid"`
			Child      uint64 `json:"conrelid"`
			Parent     uint64 `json:"confrelid"`
			Name       string `json:"conname"`
			Keys       []int  `json:"conkey"`
			ParentKeys []int  `json:"confkey"`
			Match      string `json:"confmatchtype"`
			Update     string `json:"confupdtype"`
			Delete     string `json:"confdeltype"`
			Validated  bool   `json:"convalidated"`
			Deferrable bool   `json:"condeferrable"`
			Deferred   bool   `json:"condeferred"`
		}
		if json.Unmarshal(entry.Data, &fk) != nil {
			return out, ErrConflict
		}
		if fk.Child != child.RelationOID {
			continue
		}
		row, err := gaussJSON(entry.Data)
		// Native 6.0 stores oid as a system column; row_to_json does not
		// include it. The separately captured entry owns this identity.
		if err != nil || row["oid"] != nil && fk.OID != entry.OID || fk.Parent != parent.RelationOID || row["contype"] != "f" || selected[entry.OID] || constraints[entry.OID] == nil || Signature(row) != Signature(constraints[entry.OID]) || PostgreSQLIdentifier(fk.Name) != nil || len(fk.Keys) == 0 || len(fk.Keys) != len(fk.ParentKeys) || fk.Deferred && !fk.Deferrable {
			return out, ErrConflict
		}
		for _, field := range []string{"convalidated", "condeferrable", "condeferred"} {
			if _, ok := row[field].(bool); !ok {
				return out, ErrConflict
			}
		}
		constraint := OpenGaussIncomingConstraint{OID: entry.OID, Name: fk.Name, Match: fk.Match, Update: fk.Update, Delete: fk.Delete, Validated: fk.Validated, Deferrable: fk.Deferrable, Deferred: fk.Deferred}
		seenChild, seenParent := map[int]bool{}, map[int]bool{}
		for i, number := range fk.Keys {
			parentNumber := fk.ParentKeys[i]
			if columns[number] == "" || seenChild[number] || seenParent[parentNumber] {
				return out, ErrConflict
			}
			seenChild[number], seenParent[parentNumber] = true, true
			constraint.Columns = append(constraint.Columns, columns[number])
			constraint.ColumnNumbers = append(constraint.ColumnNumbers, number)
			found := false
			for _, column := range original.Table.Columns() {
				if column.OpenGauss != nil && column.OpenGauss.Number == parentNumber {
					constraint.ParentColumns = append(constraint.ParentColumns, column.Name)
					constraint.ParentIDs = append(constraint.ParentIDs, column.ID)
					found = true
				}
			}
			if !found {
				return out, ErrConflict
			}
		}
		selected[entry.OID] = true
		out.constraints = append(out.constraints, constraint)
	}
	if len(selected) == 0 {
		return out, ErrConflict
	}
	for oid, row := range constraints {
		parentOID, err := gaussNumber(row["confrelid"])
		if err != nil {
			return out, err
		}
		if row["contype"] == "f" && parentOID == parent.RelationOID && !selected[oid] {
			return out, ErrConflict
		}
	}
	sort.Slice(out.constraints, func(i, j int) bool { return out.constraints[i].OID < out.constraints[j].OID })
	out.original, out.desired, out.revision, out.child = Signature(original), Signature(desired), revision, child.Clone()
	return out, nil
}

func VerifyOpenGaussIncomingUnchanged(baseline OpenGaussIncomingBaseline, original Snapshot, desired Table, actual *OpenGaussCatalog) error {
	if baseline.child == nil || actual == nil || baseline.original != Signature(original) || baseline.desired != Signature(desired) {
		return ErrConflict
	}
	before, e1 := baseline.child.Fingerprint()
	after, e2 := actual.Fingerprint()
	if e1 != nil || e2 != nil || before != baseline.revision || before != after {
		return errors.Join(e1, e2, ErrConflict)
	}
	return nil
}

// Reconstruct a private, empty child from selected key types and raw FK
// properties only. No default, generated expression, user program, sequence,
// index or source row is copied or evaluated. Parent keys follow stable IDs.
func OpenGaussIncomingProbeTable(baseline OpenGaussIncomingBaseline, original Snapshot, desired Table, name, parentName string, fields map[string]PostgreSQLField) (Table, OpenGaussDesignNames, error) {
	table := Table{Name: name, OpenGauss: &OpenGaussTableProperties{Persistence: "p"}}
	names := OpenGaussDesignNames{Columns: map[string]string{}, Constraints: map[string]string{}}
	if baseline.child == nil || baseline.original != Signature(original) || baseline.desired != Signature(desired) || PostgreSQLIdentifier(name) != nil || PostgreSQLIdentifier(parentName) != nil || name == parentName {
		return table, names, ErrConflict
	}
	nextNames := map[string]string{}
	for _, column := range desired.Columns() {
		nextNames[column.ID] = column.Name
	}
	actions := map[string]string{"a": "NO ACTION", "c": "CASCADE", "r": "RESTRICT", "n": "SET NULL", "d": "SET DEFAULT"}
	for _, constraint := range baseline.constraints {
		var keys, parentKeys []string
		for i, column := range constraint.Columns {
			keys = append(keys, Quote(column))
			parentColumn := nextNames[constraint.ParentIDs[i]]
			if parentColumn == "" {
				return table, names, errors.New("native incoming reference targets a removed parent key")
			}
			parentKeys = append(parentKeys, Quote(parentColumn))
			if names.Columns[column] != "" {
				continue
			}
			field, ok := fields[column]
			if !ok {
				return table, names, ErrConflict
			}
			switch strings.ToLower(field.Type) {
			case "serial", "serial2", "serial4", "serial8", "bigserial", "smallserial":
				return table, names, ErrUnsupported
			}
			declaration, err := (PostgreSQLField{Type: field.Type, Collation: field.Collation}).SQL()
			if err != nil {
				return table, names, err
			}
			id := "incoming-column:" + strconv.FormatUint(baseline.child.RelationOID, 10) + ":" + column
			table.Clauses = append(table.Clauses, Clause{ID: id, Kind: "column", Name: column, SQL: declaration})
			names.Columns[column] = id
		}
		// Native 6.0's FKCONSTR_MATCH_UNSPECIFIED is 'u', including SQL
		// MATCH SIMPLE. PostgreSQL's 's' is not native catalog evidence.
		match := map[string]string{"u": "SIMPLE", "f": "FULL"}[constraint.Match]
		if match == "" || actions[constraint.Update] == "" || actions[constraint.Delete] == "" {
			return table, names, ErrUnsupported
		}
		private := fmt.Sprintf("%s_f_%d", name, len(names.Constraints))
		text := "CONSTRAINT " + Quote(private) + " FOREIGN KEY(" + strings.Join(keys, ",") + ") REFERENCES " + Quote(original.OpenGauss.Namespace) + "." + Quote(parentName) + "(" + strings.Join(parentKeys, ",") + ") MATCH " + match + " ON UPDATE " + actions[constraint.Update] + " ON DELETE " + actions[constraint.Delete]
		if constraint.Deferrable {
			text += " DEFERRABLE"
			if constraint.Deferred {
				text += " INITIALLY DEFERRED"
			}
		}
		if !constraint.Validated {
			text += " NOT VALID"
		}
		id := "incoming:" + strconv.FormatUint(constraint.OID, 10)
		table.Clauses = append(table.Clauses, Clause{ID: id, Kind: "constraint", Name: private, SQL: text})
		names.Constraints[private] = id
	}
	return table, names, nil
}
