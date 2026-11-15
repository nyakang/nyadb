package rqlite

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func validName(name string) bool {
	return name != "" && len(name) <= 4096 && utf8.ValidString(name) && !strings.ContainsRune(name, 0)
}
func quote(name string) string { return `"` + strings.ReplaceAll(name, `"`, `""`) + `"` }

// An explicit byte-array binding plus CAST avoids the native parser's X'..'
// string-to-BLOB heuristic. []byte itself would be JSON base64, not an array.
func textParameter(value string) []int {
	out := make([]int, len(value))
	for i := range value {
		out[i] = int(value[i])
	}
	return out
}
func namedStatement(sql, name string) []any { return []any{sql, textParameter(name)} }

func (s *Session) checkNamespace(n contract.NamespaceRef) error {
	if !s.Alive() {
		return contract.ErrRevoked
	}
	if n.ProfileID != s.ProfileID() || n.Catalog != "main" || n.Schema != "" {
		return connections.ErrInvalid
	}
	return nil
}
func (s *Session) checkObject(o contract.ObjectRef) error {
	if err := s.checkNamespace(o.Namespace()); err != nil {
		return err
	}
	if !validName(o.Name) || o.Kind != "table" && o.Kind != "view" {
		return contract.ErrUnsupported
	}
	return nil
}
func (s *Session) Namespaces(ctx context.Context) ([]contract.Namespace, error) {
	if !s.Alive() {
		return nil, contract.ErrRevoked
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return []contract.Namespace{{Ref: contract.NamespaceRef{ProfileID: s.ProfileID(), Catalog: "main"}, Label: "main"}}, nil
}
func decodedRows(r receipt, index, columns int) ([][]contract.Cell, error) {
	if index >= len(r.Results) || r.Results[index].failed() || len(r.Results[index].Columns) != columns || len(r.Results[index].Values) > 100000 {
		return nil, errIncomplete
	}
	var out [][]contract.Cell
	for _, raw := range r.Results[index].Values {
		row, err := r.Results[index].cells(raw)
		if err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, nil
}
func cellText(c contract.Cell) (string, error) {
	if c.Kind != contract.Text {
		return "", errIncomplete
	}
	return c.Text, nil
}
func cellInt(c contract.Cell) (int64, error) {
	if c.Kind != contract.Integer {
		return 0, errIncomplete
	}
	n, e := strconv.ParseInt(c.Text, 10, 64)
	if e != nil {
		return 0, errIncomplete
	}
	return n, nil
}

func (s *Session) Browse(ctx context.Context, n contract.NamespaceRef) ([]contract.DBObject, error) {
	if err := s.checkNamespace(n); err != nil {
		return nil, err
	}
	r, err := s.request(ctx, "/db/query", []any{"SELECT name,type,coalesce(sql,'') FROM main.sqlite_schema WHERE type IN ('table','view') AND substr(name,1,7)<>'sqlite_' ORDER BY name"}, false, false)
	if err != nil {
		return nil, err
	}
	rows, err := decodedRows(r, 0, 3)
	if err != nil {
		return nil, err
	}
	out := make([]contract.DBObject, 0, len(rows))
	for _, row := range rows {
		name, e := cellText(row[0])
		if e != nil || !validName(name) {
			return nil, errIncomplete
		}
		kind, e := cellText(row[1])
		if e != nil || kind != "table" && kind != "view" {
			return nil, errIncomplete
		}
		definition, e := cellText(row[2])
		if e != nil || len(definition) > 1<<20 {
			return nil, errIncomplete
		}
		label := "表"
		if kind == "view" {
			label = "视图"
		}
		out = append(out, contract.DBObject{Ref: n.Object(kind, name), Name: name, Kind: label, Engine: "RQLite", Definition: definition})
	}
	return out, nil
}

type tableSnapshot struct {
	structure *contract.TableStructure
	key       []string
}
type indexSnapshot struct {
	name, origin              string
	unique, partial, eligible bool
	key                       []string
}

func (s *Session) TableMetadata(ctx context.Context, o contract.ObjectRef) (*contract.TableStructure, error) {
	t, err := s.table(ctx, o)
	if err != nil {
		return nil, err
	}
	return t.structure, nil
}

func (s *Session) table(ctx context.Context, o contract.ObjectRef) (tableSnapshot, error) {
	if err := s.checkObject(o); err != nil {
		return tableSnapshot{}, err
	}
	stmts := []any{
		namedStatement("SELECT type,sql FROM main.sqlite_schema WHERE type IN ('table','view') AND name=CAST(? AS TEXT)", o.Name),
		namedStatement(`SELECT cid,name,type,"notnull",dflt_value,pk,hidden FROM pragma_table_xinfo(CAST(? AS TEXT),'main') ORDER BY cid`, o.Name),
		namedStatement(`SELECT il.name,il."unique",il.origin,il.partial,ix.seqno,ix.cid,ix.name,ix.desc,ix.coll,ix.key FROM pragma_index_list(CAST(? AS TEXT),'main') il LEFT JOIN pragma_index_xinfo(il.name,'main') ix ORDER BY il.seq,ix.seqno`, o.Name),
		namedStatement(`SELECT id,seq,"table","from","to",on_update,on_delete FROM pragma_foreign_key_list(CAST(? AS TEXT),'main') ORDER BY id,seq`, o.Name),
		namedStatement("SELECT name,type,sql FROM main.sqlite_schema WHERE tbl_name=CAST(? AS TEXT) AND type IN ('index','trigger') AND sql IS NOT NULL ORDER BY type,name", o.Name),
	}
	r, err := s.request(ctx, "/db/query", stmts, true, false)
	if err != nil {
		return tableSnapshot{}, err
	}
	info, err := decodedRows(r, 0, 2)
	if err != nil || len(info) != 1 {
		return tableSnapshot{}, connections.ErrNotFound
	}
	kind, err := cellText(info[0][0])
	if err != nil || kind != o.Kind {
		return tableSnapshot{}, connections.ErrInvalid
	}
	definition, err := cellText(info[0][1])
	if err != nil || len(definition) > 1<<20 {
		return tableSnapshot{}, errIncomplete
	}
	t := &contract.TableStructure{Object: o, Database: "main", Name: o.Name, Kind: kind, Engine: "RQLite", Definition: definition}
	columns, err := decodedRows(r, 1, 7)
	if err != nil || len(columns) > 2000 {
		return tableSnapshot{}, errIncomplete
	}
	primary := map[int64]string{}
	byName := map[string]int{}
	for _, row := range columns {
		cid, e := cellInt(row[0])
		if e != nil || cid < 0 {
			return tableSnapshot{}, errIncomplete
		}
		name, e := cellText(row[1])
		if e != nil || !validName(name) {
			return tableSnapshot{}, errIncomplete
		}
		typ, e := cellText(row[2])
		if e != nil {
			return tableSnapshot{}, errIncomplete
		}
		notnull, e := cellInt(row[3])
		if e != nil {
			return tableSnapshot{}, errIncomplete
		}
		pk, e := cellInt(row[5])
		if e != nil || pk < 0 {
			return tableSnapshot{}, errIncomplete
		}
		hidden, e := cellInt(row[6])
		if e != nil || hidden < 0 || hidden > 3 {
			return tableSnapshot{}, errIncomplete
		}
		if hidden == 1 {
			continue
		}
		col := contract.StructureColumn{Name: name, Type: typ, DataType: typ, Nullable: notnull == 0, Position: cid + 1, Generated: hidden == 2 || hidden == 3, ValueKind: typeKind(typ)}
		if row[4].Kind != contract.Null {
			value, e := cellText(row[4])
			if e != nil {
				return tableSnapshot{}, e
			}
			col.Default = &value
		}
		if pk > 0 {
			if primary[pk] != "" {
				return tableSnapshot{}, errIncomplete
			}
			primary[pk] = name
			col.Key = "PRI"
		}
		if _, duplicate := byName[name]; duplicate {
			return tableSnapshot{}, errIncomplete
		}
		byName[name] = len(t.Columns)
		t.Columns = append(t.Columns, col)
	}
	indexes, err := decodedRows(r, 2, 10)
	if err != nil {
		return tableSnapshot{}, err
	}
	var privateIndexes []indexSnapshot
	indexedPrimary := false
	for _, row := range indexes {
		name, e := cellText(row[0])
		if e != nil {
			return tableSnapshot{}, e
		}
		unique, e := cellInt(row[1])
		if e != nil {
			return tableSnapshot{}, e
		}
		origin, e := cellText(row[2])
		if e != nil {
			return tableSnapshot{}, e
		}
		partial, e := cellInt(row[3])
		if e != nil {
			return tableSnapshot{}, e
		}
		if origin == "pk" {
			indexedPrimary = true
		}
		if len(privateIndexes) == 0 || privateIndexes[len(privateIndexes)-1].name != name {
			privateIndexes = append(privateIndexes, indexSnapshot{name: name, origin: origin, unique: unique == 1, partial: partial != 0, eligible: true})
			typ := "BTREE"
			if partial != 0 {
				typ += " (partial)"
			}
			t.Indexes = append(t.Indexes, contract.StructureIndex{Name: name, Type: typ, Unique: unique == 1})
		}
		ix := &privateIndexes[len(privateIndexes)-1]
		display := &t.Indexes[len(t.Indexes)-1]
		seq, e := cellInt(row[4])
		if e != nil {
			return tableSnapshot{}, e
		}
		cid, e := cellInt(row[5])
		if e != nil {
			return tableSnapshot{}, e
		}
		desc, e := cellInt(row[7])
		if e != nil {
			return tableSnapshot{}, e
		}
		key, e := cellInt(row[9])
		if e != nil {
			return tableSnapshot{}, e
		}
		if key != 1 {
			continue
		}
		colName := ""
		if row[6].Kind != contract.Null {
			colName, e = cellText(row[6])
			if e != nil {
				return tableSnapshot{}, e
			}
		}
		direction := "ASC"
		if desc == 1 {
			direction = "DESC"
		}
		display.Columns = append(display.Columns, contract.StructureIndexColumn{Name: colName, Direction: direction, Position: seq + 1})
		ci, found := byName[colName]
		if !found || cid < 0 || t.Columns[ci].Nullable || t.Columns[ci].Generated {
			ix.eligible = false
		}
		ix.key = append(ix.key, colName)
	}
	if len(primary) == 1 && !indexedPrimary {
		name := primary[1]
		if i, ok := byName[name]; ok && strings.EqualFold(t.Columns[i].Type, "INTEGER") {
			t.Columns[i].Nullable = false
		}
	}
	var chosen []string
	for i := int64(1); i <= int64(len(primary)); i++ {
		name := primary[i]
		ci, ok := byName[name]
		if !ok || t.Columns[ci].Nullable || t.Columns[ci].Generated {
			chosen = nil
			break
		}
		chosen = append(chosen, name)
	}
	if len(chosen) == 0 {
		for _, ix := range privateIndexes {
			if ix.unique && !ix.partial && ix.eligible && len(ix.key) > 0 {
				chosen = append([]string(nil), ix.key...)
				break
			}
		}
	}
	foreign, err := decodedRows(r, 3, 7)
	if err != nil {
		return tableSnapshot{}, err
	}
	lastID := int64(-1)
	for _, row := range foreign {
		id, e := cellInt(row[0])
		if e != nil || id < 0 {
			return tableSnapshot{}, errIncomplete
		}
		seq, e := cellInt(row[1])
		if e != nil || seq < 0 {
			return tableSnapshot{}, errIncomplete
		}
		refTable, e := cellText(row[2])
		if e != nil {
			return tableSnapshot{}, e
		}
		from, e := cellText(row[3])
		if e != nil {
			return tableSnapshot{}, e
		}
		to := ""
		if row[4].Kind != contract.Null {
			to, e = cellText(row[4])
			if e != nil {
				return tableSnapshot{}, e
			}
		}
		update, e := cellText(row[5])
		if e != nil {
			return tableSnapshot{}, e
		}
		deleteAction, e := cellText(row[6])
		if e != nil {
			return tableSnapshot{}, e
		}
		if id != lastID {
			t.ForeignKeys = append(t.ForeignKeys, contract.StructureForeignKey{Name: fmt.Sprintf("fk_%d", id), ReferencedDatabase: "main", ReferencedTable: refTable, OnUpdate: update, OnDelete: deleteAction})
			lastID = id
		}
		fk := &t.ForeignKeys[len(t.ForeignKeys)-1]
		fk.Columns = append(fk.Columns, contract.StructureForeignKeyColumn{Name: from, ReferencedName: to, Position: seq + 1})
	}
	definitions, err := decodedRows(r, 4, 3)
	if err != nil || len(definitions) > 1000 {
		return tableSnapshot{}, errIncomplete
	}
	for _, row := range definitions {
		name, e := cellText(row[0])
		if e != nil {
			return tableSnapshot{}, e
		}
		typ, e := cellText(row[1])
		if e != nil {
			return tableSnapshot{}, e
		}
		sql, e := cellText(row[2])
		if e != nil || len(sql) > 1<<20 {
			return tableSnapshot{}, errIncomplete
		}
		t.Definitions = append(t.Definitions, contract.Definition{Name: name, Kind: typ, SQL: sql})
	}
	return tableSnapshot{structure: t, key: chosen}, nil
}

func typeKind(typ string) contract.CellKind {
	typ = strings.ToUpper(typ)
	if strings.Contains(typ, "INT") {
		return contract.Integer
	}
	if strings.Contains(typ, "CHAR") || strings.Contains(typ, "CLOB") || strings.Contains(typ, "TEXT") {
		return contract.Text
	}
	if strings.Contains(typ, "BLOB") || typ == "" {
		return contract.Binary
	}
	if strings.Contains(typ, "REAL") || strings.Contains(typ, "FLOA") || strings.Contains(typ, "DOUB") {
		return contract.Floating
	}
	return contract.Decimal
}
