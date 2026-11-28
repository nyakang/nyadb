package manticore

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

const dataPreviewUnits = 1024
const dataPageBytes = 8 << 20

func (d *dataLease) load(ctx context.Context, id int64, database, table string, offset int64, limit int) (*contract.DataPage, error) {
	return d.page(ctx, id, database, table, contract.DataOptions{Offset: offset, Limit: limit})
}
func dataCells(columns []contract.StructureColumn, wireColumns []contract.Column, row []contract.Cell, preview bool) ([]contract.Cell, error) {
	if len(columns) != len(row) || len(columns) != len(wireColumns) {
		return nil, contract.ErrDataPage
	}
	result := make([]contract.Cell, len(row))
	for i, c := range row {
		if wireColumns[i].Name != columns[i].Name || c.Truncated {
			return nil, contract.ErrDataPage
		}
		c = c.Clone()
		// Metadata enriches JSON only when the original wire text is valid JSON.
		// An omitted Manticore JSON attribute can be an empty original CHAR value;
		// it must not be relabelled as SQL NULL or a fabricated empty object.
		if columns[i].DataType == "json" && c.Kind == contract.Text && c.Text != "" {
			if !json.Valid([]byte(c.Text)) {
				return nil, contract.ErrDataPage
			}
			c.Kind = contract.JSON
		}
		if preview {
			if c.Kind == contract.Binary && len(c.Bytes) > dataPreviewUnits {
				c.Bytes = append([]byte(nil), c.Bytes[:dataPreviewUnits]...)
				c.Truncated = true
			}
			if c.Kind == contract.Text || c.Kind == contract.JSON {
				if !utf8.ValidString(c.Text) {
					return nil, contract.ErrDataPage
				}
				units := 0
				for end := range c.Text {
					if units == dataPreviewUnits {
						c.Text = strings.Clone(c.Text[:end])
						c.Truncated = true
						break
					}
					units++
				}
			}
		}
		result[i] = c
	}
	return result, nil
}
func (d *dataLease) page(ctx context.Context, id int64, database, table string, o contract.DataOptions) (*contract.DataPage, error) {
	if e := d.identity(id, database, table); e != nil {
		return nil, e
	}
	o, e := captureOptions(o, d.object)
	if e != nil {
		return nil, e
	}
	if o.Limit < 1 || o.Limit > contract.DataMaxPageSize || o.Offset < 0 {
		return nil, connections.ErrInvalid
	}
	child, done, e := d.request(ctx)
	if e != nil {
		return nil, e
	}
	defer done()
	start := time.Now()
	m, e := d.table(child)
	if e != nil {
		return nil, e
	}
	q, e := selectData(m, o, nil, o.Offset, o.Limit+1)
	if e != nil {
		return nil, e
	}
	d.mu.Lock()
	if d.pageSequence == ^uint64(0) {
		d.mu.Unlock()
		return nil, contract.ErrDataPage
	}
	d.pageSequence++
	sequence := d.pageSequence
	d.mu.Unlock()
	grants := map[string]*dataGrant{}
	editable := len(writeKinds(m)) > 0
	page := &contract.DataPage{Object: d.object, Editable: &editable, Database: database, Table: table, Offset: o.Offset, Limit: o.Limit, ColumnMetadata: m.Columns, EditKinds: writeKinds(m), ColumnEditKinds: writePermissions(m), FilterColumns: []string{}, SortColumns: []string{}, QuerySQL: q.display, LiveRead: true}
	for _, c := range m.Columns {
		page.Columns = append(page.Columns, c.Name)
		page.ColumnKinds = append(page.ColumnKinds, c.DataType)
		if _, err := nativeLiteral(c, sortProbeValue(c)); err == nil {
			page.FilterColumns = append(page.FilterColumns, c.Name)
			page.SortColumns = append(page.SortColumns, c.Name)
		}
	}
	budget := dataPageBytes
	_, e = d.primary.ExecuteStream(child, database, q.sql, func(_ int, columns []contract.Column, row []contract.Cell) error {
		if len(page.Rows) > o.Limit {
			return contract.ErrDataPage
		}
		full, e := dataCells(m.Columns, columns, row, false)
		if e != nil {
			return e
		}
		cells, e := dataCells(m.Columns, columns, row, true)
		if e != nil {
			return e
		}
		var values []contract.DataCell
		for _, cell := range cells {
			budget -= len(cell.Text) + len(cell.Bytes) + 32
			if budget < 0 {
				return contract.ErrDataPage
			}
			v := cell
			values = append(values, contract.DataCell{Value: &v, Text: v.Display(), Null: v.Kind == contract.Null, Truncated: v.Truncated})
		}
		token, g, e := d.pageGrant(m, full, cells)
		if e != nil {
			return e
		}
		grants[token] = g
		published := snapshotCopy(g.preview)
		page.RowRefs = append(page.RowRefs, &published.Ref)
		page.RowSnapshots = append(page.RowSnapshots, snapshotCopy(g.preview))
		page.Rows = append(page.Rows, values)
		return nil
	})
	if e != nil {
		return nil, e
	}
	page.HasMore = len(page.Rows) > o.Limit
	if page.HasMore {
		delete(grants, string(page.RowRefs[o.Limit].Values[0]))
		page.RowRefs = page.RowRefs[:o.Limit]
		page.RowSnapshots = page.RowSnapshots[:o.Limit]
		page.Rows = page.Rows[:o.Limit]
	}
	d.mu.Lock()
	if d.closed || child.Err() != nil || sequence != d.pageSequence {
		d.mu.Unlock()
		return nil, contract.ErrDataConflict
	}
	d.grants = grants
	d.mu.Unlock()
	page.CompletedAt = time.Now()
	page.Duration = time.Since(start)
	return page, nil
}
func (d *dataLease) count(ctx context.Context, id int64, database, table string) (int64, error) {
	if e := d.identity(id, database, table); e != nil {
		return 0, e
	}
	child, done, e := d.request(ctx)
	if e != nil {
		return 0, e
	}
	defer done()
	m, e := d.table(child)
	if e != nil {
		return 0, e
	}
	owner, e := d.auxiliary()
	if e != nil {
		return 0, e
	}
	defer owner.Release()
	r, e := owner.Execute(child, database, "SELECT COUNT(*) FROM "+quote(m.Object.Catalog)+"."+quote(m.Name))
	if e != nil {
		return 0, e
	}
	if len(r.Sets) != 1 || len(r.Sets[0].Rows) != 1 || len(r.Sets[0].Rows[0]) != 1 {
		return 0, contract.ErrDataPage
	}
	n, e := strconv.ParseInt(r.Sets[0].Rows[0][0].Text, 10, 64)
	if e != nil || n < 0 {
		return 0, contract.ErrDataPage
	}
	return n, nil
}
func (d *dataLease) info(ctx context.Context, id int64, database, table string) (*contract.TableInfo, error) {
	if e := d.identity(id, database, table); e != nil {
		return nil, e
	}
	child, done, e := d.request(ctx)
	if e != nil {
		return nil, e
	}
	defer done()
	m, e := d.table(child)
	if e != nil {
		return nil, e
	}
	owner, e := d.auxiliary()
	if e != nil {
		return nil, e
	}
	defer owner.Release()
	r, e := owner.Execute(child, database, "SHOW TABLE "+quote(m.Name)+" STATUS")
	if e != nil {
		return nil, e
	}
	if len(r.Sets) != 1 || r.Truncated {
		return nil, contract.ErrDataPage
	}
	info := &contract.TableInfo{Database: database, Name: table, Kind: "TABLE", Engine: m.Engine}
	for _, row := range r.Sets[0].Rows {
		if len(row) != 2 {
			return nil, contract.ErrDataPage
		}
		switch row[0].Text {
		case "indexed_documents", "stored_queries", "disk_bytes":
			n, e := strconv.ParseInt(row[1].Text, 10, 64)
			if e != nil || n < 0 {
				return nil, contract.ErrDataPage
			}
			if row[0].Text == "indexed_documents" && m.Engine != "percolate" || row[0].Text == "stored_queries" && m.Engine == "percolate" {
				info.EstimatedRows = &n
			} else if row[0].Text == "disk_bytes" {
				info.DataLength = &n
			}
		}
	}
	return info, nil
}
