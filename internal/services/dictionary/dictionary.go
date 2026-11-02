// Package dictionary builds owned, read-only metadata documents. It never
// reads table rows, executes user SQL, or derives identity from display labels.
package dictionary

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/nyakang/nyadb/internal/db/contract"
)

const MaxTables = 500
const MaxItems = 50000
const MaxBytes = 8 << 20

var ErrLimit = errors.New("data dictionary exceeds its metadata limit")

type Reader struct {
	Browse   func(context.Context, contract.NamespaceRef) ([]contract.DBObject, error)
	Metadata func(context.Context, contract.ObjectRef) (*contract.TableStructure, error)
}

type Column struct {
	Name, Type, Default, Key, Extra, Comment string
	Position                                 int64
	Nullable, DefaultReported, Generated     bool
}
type Index struct {
	Name, Type, Columns string
	Unique              bool
}
type ForeignKey struct{ Name, Columns, Reference, ReferencedColumns, OnUpdate, OnDelete string }
type Table struct {
	Object                contract.ObjectRef
	Kind, Engine, Comment string
	Columns               []Column
	Indexes               []Index
	ForeignKeys           []ForeignKey
}
type Document struct {
	Namespace contract.NamespaceRef
	Tables    []Table
}

// Build returns either a complete document for the requested scope or no
// document. Each reader is already bound to the original session generation.
func Build(ctx context.Context, r Reader, namespace contract.NamespaceRef, only *contract.ObjectRef) (Document, error) {
	if namespace.ProfileID <= 0 || namespace.Catalog == "" && namespace.Schema == "" || r.Metadata == nil {
		return Document{}, contract.ErrUnsupported
	}
	if err := ctx.Err(); err != nil {
		return Document{}, err
	}
	refs := []contract.ObjectRef{}
	if only != nil {
		if only.Namespace() != namespace || only.Name == "" || !strings.EqualFold(only.Kind, "table") && !strings.EqualFold(only.Kind, "view") {
			return Document{}, contract.ErrRevoked
		}
		refs = append(refs, *only)
	} else {
		if r.Browse == nil {
			return Document{}, contract.ErrUnsupported
		}
		objects, err := r.Browse(ctx, namespace)
		if err != nil {
			return Document{}, err
		}
		for _, object := range objects {
			kind := strings.ToLower(object.Kind)
			if kind == "表" {
				kind = "table"
			}
			if kind == "视图" {
				kind = "view"
			}
			if kind != "table" && kind != "view" {
				continue
			}
			ref := object.Ref
			if ref == (contract.ObjectRef{}) {
				ref = namespace.Object(kind, object.Name)
			}
			if ref.Namespace() != namespace || ref.Name == "" || ref.Name != object.Name || !strings.EqualFold(ref.Kind, kind) {
				return Document{}, contract.ErrRevoked
			}
			refs = append(refs, ref)
			if len(refs) > MaxTables {
				return Document{}, ErrLimit
			}
		}
	}
	sort.Slice(refs, func(i, j int) bool {
		if refs[i].Name != refs[j].Name {
			return refs[i].Name < refs[j].Name
		}
		return refs[i].Kind < refs[j].Kind
	})
	doc := Document{Namespace: namespace}
	items, used := 0, len(namespace.Catalog)+len(namespace.Schema)
	for i, ref := range refs {
		if err := ctx.Err(); err != nil {
			return Document{}, err
		}
		if i > 0 && refs[i-1] == ref {
			return Document{}, contract.ErrRevoked
		}
		metadata, err := r.Metadata(ctx, ref)
		if err != nil {
			return Document{}, err
		}
		if metadata == nil || metadata.Object != ref || metadata.Name != ref.Name {
			return Document{}, contract.ErrRevoked
		}
		t := Table{Object: ref, Kind: strings.ToLower(ref.Kind), Engine: metadata.Engine, Comment: metadata.Comment}
		used += len(ref.Name) + len(t.Engine) + len(t.Comment)
		items += len(metadata.Columns) + len(metadata.Indexes) + len(metadata.ForeignKeys)
		for _, index := range metadata.Indexes {
			items += len(index.Columns)
		}
		for _, key := range metadata.ForeignKeys {
			items += len(key.Columns)
		}
		if items > MaxItems || used > MaxBytes {
			return Document{}, ErrLimit
		}
		for _, c := range metadata.Columns {
			col := Column{Name: c.Name, Type: c.Type, Position: c.Position, Nullable: c.Nullable, Key: c.Key, Extra: c.Extra, Comment: c.Comment, Generated: c.Generated}
			if col.Type == "" {
				col.Type = c.DataType
			}
			if c.Default != nil {
				col.DefaultReported, col.Default = true, *c.Default
			}
			used += len(col.Name) + len(col.Type) + len(col.Default) + len(col.Key) + len(col.Extra) + len(col.Comment)
			if used > MaxBytes {
				return Document{}, ErrLimit
			}
			t.Columns = append(t.Columns, col)
		}
		for _, index := range metadata.Indexes {
			columns := make([]string, 0, len(index.Columns))
			for _, c := range index.Columns {
				name := c.Name
				if c.SubPart != nil {
					name += fmt.Sprintf("(%d)", *c.SubPart)
				}
				if c.Direction != "" {
					name += " " + c.Direction
				}
				columns = append(columns, name)
			}
			v := Index{Name: index.Name, Type: index.Type, Unique: index.Unique, Columns: strings.Join(columns, ", ")}
			used += len(v.Name) + len(v.Type) + len(v.Columns)
			if used > MaxBytes {
				return Document{}, ErrLimit
			}
			t.Indexes = append(t.Indexes, v)
		}
		for _, fk := range metadata.ForeignKeys {
			columns, targets := []string{}, []string{}
			for _, c := range fk.Columns {
				columns = append(columns, c.Name)
				targets = append(targets, c.ReferencedName)
			}
			v := ForeignKey{Name: fk.Name, Columns: strings.Join(columns, ", "), Reference: fk.ReferencedDatabase + " / " + fk.ReferencedTable, ReferencedColumns: strings.Join(targets, ", "), OnUpdate: fk.OnUpdate, OnDelete: fk.OnDelete}
			used += len(v.Name) + len(v.Columns) + len(v.Reference) + len(v.ReferencedColumns) + len(v.OnUpdate) + len(v.OnDelete)
			if used > MaxBytes {
				return Document{}, ErrLimit
			}
			t.ForeignKeys = append(t.ForeignKeys, v)
		}
		doc.Tables = append(doc.Tables, t)
	}
	if err := ctx.Err(); err != nil {
		return Document{}, err
	}
	return doc, nil
}

func (d Document) Clone() Document {
	out := Document{Namespace: d.Namespace, Tables: append([]Table(nil), d.Tables...)}
	for i := range out.Tables {
		out.Tables[i].Columns = append([]Column(nil), d.Tables[i].Columns...)
		out.Tables[i].Indexes = append([]Index(nil), d.Tables[i].Indexes...)
		out.Tables[i].ForeignKeys = append([]ForeignKey(nil), d.Tables[i].ForeignKeys...)
	}
	return out
}
