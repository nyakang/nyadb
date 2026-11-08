package cassandra

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
)

// A position names the original native page start plus a bounded in-page skip.
// Short/empty native pages do not shorten a UI page. No result cache or hidden
// OFFSET scan is needed to repeat a boundary; repeated reads remain live.
type dataPosition struct {
	Native       bool
	Request, MAC [32]byte
	State        []byte
	Skip         int
}
type dataBoundary struct {
	Epoch    uint64
	Position dataPosition
}

func dataOptions(o contract.DataOptions) error {
	if o.Limit < 1 || o.Limit > 1000 || o.Offset < 0 {
		return ErrBounds
	}
	if o.Condition != nil || len(o.Filters) > 0 || len(o.Sorts) > 0 || o.FilterColumn != "" || o.FilterValue != "" || o.FilterMode != "" || o.SortColumn != "" || o.SortDescending {
		return contract.ErrUnsupported
	}
	return nil
}
func dataSelect(o contract.ObjectRef, b *dataBinding) string {
	names := make([]string, len(b.Names))
	for i, n := range b.Names {
		names[i] = quoteName(n)
	}
	return "SELECT " + strings.Join(names, ", ") + " FROM " + quoteName(o.Catalog) + "." + quoteName(o.Name)
}
func (l *dataLease) boundary(offset int64, p dataPosition) *contract.DataCursor {
	raw, _ := json.Marshal(dataBoundary{l.epoch, p})
	c := &contract.DataCursor{Database: l.object.Catalog, Table: l.object.Name, Offset: offset, Columns: []string{"_cassandra_native_cursor"}, Descending: []bool{false}, Values: [][]byte{raw}}
	c.RuleSignature = l.sign(struct {
		Cursor  *contract.DataCursor
		Limit   int
		Binding string
	}{c, l.limit, l.binding.Signature})
	return c
}
func (l *dataLease) decodeBoundary(o contract.DataOptions) (dataPosition, error) {
	c := o.After
	if c == nil || c.Database != l.object.Catalog || c.Table != l.object.Name || c.Offset != o.Offset || o.Limit != l.limit || len(c.Values) != 1 || len(c.Values[0]) > 128<<10 || !slices.Equal(c.Columns, []string{"_cassandra_native_cursor"}) || !slices.Equal(c.Descending, []bool{false}) || c.FilterColumn != "" || c.FilterValue != "" || c.FilterMode != "" || c.SortColumn != "" || c.SortDescending {
		return dataPosition{}, ErrInvalid
	}
	copy := *c
	copy.RuleSignature = ""
	expected := l.sign(struct {
		Cursor  *contract.DataCursor
		Limit   int
		Binding string
	}{&copy, l.limit, l.binding.Signature})
	if !hmac.Equal([]byte(c.RuleSignature), []byte(expected)) {
		return dataPosition{}, ErrInvalid
	}
	var v dataBoundary
	if json.Unmarshal(c.Values[0], &v) != nil || v.Epoch != l.epoch || v.Position.Skip < 0 || v.Position.Skip > MaxRows || len(v.Position.State) > maxPageState {
		return dataPosition{}, ErrInvalid
	}
	if !v.Position.Native && (len(v.Position.State) > 0 || v.Position.Request != [32]byte{} || v.Position.MAC != [32]byte{}) {
		return dataPosition{}, ErrInvalid
	}
	v.Position.State = bytes.Clone(v.Position.State)
	return v.Position, nil
}
func nativePosition(c *Cursor) dataPosition {
	if c == nil {
		return dataPosition{}
	}
	return dataPosition{Native: true, Request: c.request, MAC: c.mac, State: bytes.Clone(c.state)}
}
func (p dataPosition) cursor(c *Client) *Cursor {
	if !p.Native {
		return nil
	}
	return &Cursor{owner: c, request: p.Request, mac: p.MAC, state: bytes.Clone(p.State)}
}
func validateDataColumns(cols []Column, o contract.ObjectRef, b *dataBinding) error {
	if len(cols) != len(b.Names) {
		return ErrMetadataChanged
	}
	for i, c := range cols {
		if c.Keyspace != o.Catalog || c.Table != o.Name || c.Name != b.Names[i] || c.Type == nil {
			return ErrMetadataChanged
		}
	}
	return nil
}

// Each callback must own its output. Native result memory is bounded by the
// transport. The page accumulator additionally caps preview and identity memory.
func dataBatch(ctx context.Context, c *Client, o contract.ObjectRef, b *dataBinding, p dataPosition, limit int, columns *[]Column, visit func([]Value) error) (dataPosition, bool, error) {
	req := Request{Statement: dataSelect(o, b), PageSize: limit}
	seen := map[[32]byte]bool{}
	collected := 0
	for pages := 0; pages < 1024; pages++ {
		if err := ctx.Err(); err != nil {
			return p, false, err
		}
		token := sha256.Sum256(append([]byte{byteBool(p.Native)}, p.State...))
		if seen[token] {
			return p, false, ErrProtocol
		}
		seen[token] = true
		req.Cursor = p.cursor(c)
		r, err := c.execute(ctx, req, true)
		if err != nil {
			return p, false, err
		}
		if r.Kind != 2 {
			return p, false, ErrProtocol
		}
		if err = validateDataColumns(r.Columns, o, b); err != nil {
			return p, false, err
		}
		if *columns != nil && !reflect.DeepEqual(*columns, r.Columns) {
			return p, false, ErrMetadataChanged
		}
		if *columns == nil {
			*columns = r.Columns
		}
		if p.Skip > len(r.Rows) {
			return p, false, contract.ErrDataConflict
		}
		for i := p.Skip; i < len(r.Rows); i++ {
			if collected == limit {
				p.Skip = i
				return p, true, nil
			}
			if len(r.Rows[i]) != len(b.Names) {
				return p, false, ErrProtocol
			}
			if err = visit(r.Rows[i]); err != nil {
				return p, false, err
			}
			collected++
		}
		if r.Next == nil {
			return dataPosition{}, false, ctx.Err()
		}
		p = nativePosition(r.Next)
		if collected == limit {
			return p, true, nil
		}
	}
	return p, false, ErrBounds
}

func byteBool(v bool) byte {
	if v {
		return 1
	}
	return 0
}
func (l *dataLease) page(ctx context.Context, id int64, db, name string, o contract.DataOptions) (*contract.DataPage, error) {
	if err := l.identity(id, db, name); err != nil {
		return nil, err
	}
	if o.Object != l.object {
		return nil, ErrInvalid
	}
	if err := dataOptions(o); err != nil {
		return nil, err
	}
	ctx, done, err := l.request(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	l.pageMu.Lock()
	defer l.pageMu.Unlock()
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	if l.pageClient == nil {
		l.pageClient, err = l.session.open(ctx)
		if err != nil {
			return nil, err
		}
	}
	c := l.pageClient
	b, err := l.guard(ctx, c)
	if err != nil {
		return nil, l.pageError(err)
	}
	p := dataPosition{}
	if o.After == nil {
		if o.Offset != 0 {
			return nil, contract.ErrUnsupported
		}
		l.epoch++
		l.limit = o.Limit
		l.pageColumns = nil
	} else {
		p, err = l.decodeBoundary(o)
		if err != nil {
			return nil, err
		}
	}
	started := time.Now()
	editable := false
	out := &contract.DataPage{Object: l.object, Database: db, Table: name, Columns: append([]string(nil), b.Names...), ColumnMetadata: cloneStructure(b.Metadata).Columns, Offset: o.Offset, Limit: o.Limit, Editable: &editable, KeysetAvailable: true, PagingMode: "cursor", LiveRead: true, QuerySQL: dataSelect(l.object, b)}
	out.CurrentCursor = l.boundary(o.Offset, p)
	budget := 8 << 20
	next, more, err := dataBatch(ctx, c, l.object, b, p, o.Limit, &l.pageColumns, func(values []Value) error {
		cells, snapshot, e := l.preview(values, b, &budget)
		if e != nil {
			return e
		}
		out.Rows = append(out.Rows, cells)
		out.RowRefs = append(out.RowRefs, &snapshot.Ref)
		out.RowSnapshots = append(out.RowSnapshots, snapshot)
		return nil
	})
	if err != nil {
		return nil, l.pageError(err)
	}
	if _, err = l.guard(ctx, c); err != nil {
		return nil, l.pageError(err)
	}
	for _, col := range l.pageColumns {
		out.ColumnKinds = append(out.ColumnKinds, typeName(col.Type))
	}
	out.HasMore = more
	if more {
		out.NextCursor = l.boundary(o.Offset+int64(o.Limit), next)
	}
	out.CompletedAt, out.Duration = time.Now(), time.Since(started)
	return out, ctx.Err()
}
