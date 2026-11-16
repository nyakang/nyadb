package databend

import (
	"context"
	"encoding/json"
	"regexp"
	"strconv"
	"strings"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/sqltext"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func (s *Session) checkNamespace(n contract.NamespaceRef) error {
	if n.ProfileID != s.ProfileID() || n.Catalog != "" || !sqltext.ValidName(n.Schema) {
		return connections.ErrInvalid
	}
	if !s.Alive() {
		return contract.ErrRevoked
	}
	return nil
}
func (s *Session) checkObject(o contract.ObjectRef) error {
	if err := s.checkNamespace(o.Namespace()); err != nil {
		return err
	}
	if !sqltext.ValidName(o.Name) {
		return connections.ErrInvalid
	}
	return nil
}
func (s *Session) native(ctx context.Context, original *ManagedSession, method string, params any, target any) error {
	r, err := original.Call(ctx, method, params)
	s.observed("metadata-"+method, r)
	if err != nil {
		return err
	}
	if !r.ExecutorAcknowledged || len(r.Result) > 8<<20 || string(r.Result) == "null" || json.Unmarshal(r.Result, target) != nil {
		return contract.ErrUnsupported
	}
	return nil
}
func (s *Session) Namespaces(ctx context.Context) (out []contract.Namespace, err error) {
	err = s.withMetadata(ctx, func(child context.Context, original *ManagedSession) error {
		// The original JDBC getSchemas is scoped to its configured catalog and
		// returns only that database. Native SHOW DATABASES supplies the complete
		// visible namespace list without changing or replacing the pinned driver.
		q, err := s.query(child, original, "SHOW DATABASES", 10000)
		if err != nil {
			return err
		}
		if q.Truncated || len(q.Sets) != 1 || len(q.Sets[0].Columns) != 1 || q.Sets[0].Columns[0].Name != "databases_in_default" {
			return contract.ErrUnsupported
		}
		seen := map[string]bool{}
		for _, row := range q.Sets[0].Rows {
			if len(row) != 1 || row[0].Kind != contract.Text {
				return contract.ErrUnsupported
			}
			name := row[0].Text
			if !sqltext.ValidName(name) || seen[name] {
				return contract.ErrUnsupported
			}
			seen[name] = true
			out = append(out, contract.Namespace{Ref: s.Namespace(name), Label: name})
		}
		return nil
	})
	return
}
func (s *Session) Browse(ctx context.Context, n contract.NamespaceRef) (out []contract.DBObject, err error) {
	if err = s.checkNamespace(n); err != nil {
		return
	}
	err = s.withMetadata(ctx, func(child context.Context, original *ManagedSession) error {
		var objects []struct {
			Name, Schema, Comment string
			Type                  string `json:"object_type"`
		}
		// Unconstrained JDBC metadata also labels tenant-global built-in function
		// overloads with the requested database. Ask for qualified object kinds;
		// global function browsing needs its own native identity/source contract.
		if err := s.native(child, original, "list_objects", map[string]any{"schema": n.Schema,
			"object_types": []string{"TABLE", "BASE TABLE", "SYSTEM TABLE", "VIEW", "SYSTEM VIEW", "MATERIALIZED VIEW", "PROCEDURE"}}, &objects); err != nil {
			return err
		}
		if len(objects) > 100000 {
			return contract.ErrUnsupported
		}
		seen := map[string]bool{}
		for _, o := range objects {
			if !sqltext.ValidName(o.Name) || o.Schema != "" && o.Schema != n.Schema {
				return contract.ErrUnsupported
			}
			kind, label := "", ""
			switch o.Type {
			case "TABLE", "BASE TABLE", "SYSTEM TABLE":
				kind, label = "table", "表"
			case "VIEW", "SYSTEM VIEW", "MATERIALIZED VIEW":
				kind, label = "view", "视图"
			case "PROCEDURE":
				kind, label = "procedure", "存储过程"
			default:
				return contract.ErrUnsupported
			}
			key := kind + "\x00" + o.Name
			if seen[key] {
				return contract.ErrUnsupported
			}
			seen[key] = true
			item := contract.DBObject{Ref: n.Object(kind, o.Name), Name: o.Name, Kind: label, Engine: "Databend", Comment: o.Comment}
			if kind == "procedure" {
				var source struct {
					Name, Schema, Source string
					Type                 string `json:"object_type"`
				}
				if err := s.native(child, original, "get_object_source", map[string]string{"schema": n.Schema, "name": o.Name, "object_type": o.Type}, &source); err != nil {
					return err
				}
				if source.Name != o.Name || source.Schema != n.Schema || source.Type != o.Type || source.Source == "" {
					return contract.ErrUnsupported
				}
				item.Definition = source.Source
			}
			out = append(out, item)
		}
		return nil
	})
	return
}

func nativeKind(declaration string) contract.CellKind {
	t := strings.ToUpper(strings.TrimSpace(declaration))
	for strings.HasPrefix(t, "NULLABLE(") && strings.HasSuffix(t, ")") {
		t = strings.TrimSpace(t[len("NULLABLE(") : len(t)-1])
	}
	switch {
	case t == "VARIANT" || strings.HasPrefix(t, "ARRAY(") || strings.HasPrefix(t, "MAP("):
		return contract.JSON
	case strings.HasPrefix(t, "TUPLE("):
		return contract.Text
	case nativeInteger.MatchString(t):
		return contract.Integer
	case strings.HasPrefix(t, "DECIMAL") || strings.HasPrefix(t, "NUMERIC"):
		return contract.Decimal
	case t == "FLOAT" || t == "DOUBLE" || strings.HasPrefix(t, "FLOAT32") || strings.HasPrefix(t, "FLOAT64"):
		return contract.Floating
	case t == "BOOLEAN" || t == "BOOL":
		return contract.Boolean
	case t == "BINARY" || strings.HasPrefix(t, "VARBINARY"):
		return contract.Binary
	case t == "DATE" || strings.HasPrefix(t, "TIMESTAMP"):
		return contract.DateTime
	default:
		return contract.Text
	}
}

var nativeInteger = regexp.MustCompile(`^(U?INT(8|16|32|64)?|TINYINT|SMALLINT|INTEGER|BIGINT)( UNSIGNED)?$`)
var nativeDecimal = regexp.MustCompile(`(?i)^DECIMAL\(\s*([0-9]+)\s*,\s*([0-9]+)\s*\)$`)

func (s *Session) TableMetadata(ctx context.Context, o contract.ObjectRef) (out *contract.TableStructure, err error) {
	if err = s.checkObject(o); err != nil {
		return
	}
	if o.Kind != "table" && o.Kind != "view" {
		return nil, contract.ErrUnsupported
	}
	err = s.withMetadata(ctx, func(child context.Context, original *ManagedSession) error {
		out, err = s.readTableMetadata(child, original, o)
		return err
	})
	return
}

// Data operations read metadata on their captured connection so a request
// holding the final metadata slot never waits for a second slot.
func (s *Session) readTableMetadata(child context.Context, original *ManagedSession, o contract.ObjectRef) (*contract.TableStructure, error) {
	var out *contract.TableStructure
	err := func() error {
		name := quoteName(o.Schema) + "." + quoteName(o.Name)
		q, err := s.query(child, original, "DESCRIBE "+name, 513)
		if err != nil {
			return err
		}
		if q.Truncated || len(q.Sets) != 1 || len(q.Sets[0].Rows) == 0 || len(q.Sets[0].Rows) > 512 || len(q.Sets[0].Columns) != 5 {
			return contract.ErrUnsupported
		}
		for i, label := range []string{"Field", "Type", "Null", "Default", "Extra"} {
			if q.Sets[0].Columns[i].Name != label {
				return contract.ErrUnsupported
			}
		}
		v := &contract.TableStructure{Object: o, Database: o.Schema, Name: o.Name, Kind: "表", Engine: "Databend"}
		if o.Kind == "view" {
			v.Kind = "视图"
		}
		seen := map[string]bool{}
		for i, row := range q.Sets[0].Rows {
			if len(row) != 5 || row[0].Kind != contract.Text || row[1].Kind != contract.Text || !sqltext.ValidName(row[0].Text) || row[1].Text == "" || seen[row[0].Text] || row[2].Kind != contract.Text || row[2].Text != "YES" && row[2].Text != "NO" {
				return contract.ErrUnsupported
			}
			seen[row[0].Text] = true
			var def *string
			if row[3].Kind == contract.Text {
				text := row[3].Text
				def = &text
			} else if row[3].Kind != contract.Null {
				return contract.ErrUnsupported
			}
			if row[4].Kind != contract.Text && row[4].Kind != contract.Null {
				return contract.ErrUnsupported
			}
			v.Columns = append(v.Columns, contract.StructureColumn{Name: row[0].Text, Type: row[1].Text, DataType: row[1].Text, ValueKind: nativeKind(row[1].Text), Position: int64(i + 1), Nullable: row[2].Text == "YES", Default: def, Extra: row[4].Text, Generated: row[4].Text != ""})
			if match := nativeDecimal.FindStringSubmatch(row[1].Text); match != nil {
				precision, e1 := strconv.ParseInt(match[1], 10, 64)
				scale, e2 := strconv.ParseInt(match[2], 10, 64)
				if e1 != nil || e2 != nil || precision < 1 || precision > 76 || scale < 0 || scale > precision {
					return contract.ErrUnsupported
				}
				v.Columns[len(v.Columns)-1].NumericPrecision = &precision
				v.Columns[len(v.Columns)-1].NumericScale = &scale
			}
		}
		// Databend exposes native view definitions through SHOW CREATE TABLE;
		// SHOW CREATE VIEW is not part of this server's grammar.
		ddl, err := s.query(child, original, "SHOW CREATE TABLE "+name, 2)
		if err != nil {
			return err
		}
		if ddl.Truncated || len(ddl.Sets) != 1 || len(ddl.Sets[0].Rows) != 1 || len(ddl.Sets[0].Rows[0]) != 2 || ddl.Sets[0].Rows[0][0].Text != o.Name || ddl.Sets[0].Rows[0][1].Kind != contract.Text || ddl.Sets[0].Rows[0][1].Text == "" {
			return contract.ErrUnsupported
		}
		v.Definition = ddl.Sets[0].Rows[0][1].Text
		out = v
		return nil
	}()
	return out, err
}

var _ contract.MetadataProvider = (*Session)(nil)
