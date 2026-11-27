package databend

import (
	"context"
	"reflect"
	"strings"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/schemaddl"
)

// Verification is an observation after the original acknowledgement, not a
// transaction or a replacement receipt. There are no concurrent-DDL locks.
func (d *schemaDocument) verifyCopy(ctx context.Context, session *ManagedSession, r contract.SchemaCopyRequest, destination contract.SchemaDesignTarget) (contract.SchemaDesignSnapshot, error) {
	var empty contract.SchemaDesignSnapshot
	actual, err := d.s.readSchemaSnapshot(ctx, session, destination, d.key[:])
	if err != nil {
		return empty, err
	}
	before, _ := schemaddl.DatabendTableBody(r.Original.Schema.DDL, r.Original.Schema.Table.Name)
	after, err := schemaddl.DatabendTableBody(actual.Schema.DDL, actual.Schema.Table.Name)
	if err != nil || before != after || actual.Schema.Databend.TableID == r.Original.Schema.Databend.TableID || !reflect.DeepEqual(actual.Schema.Databend.Columns, r.Original.Schema.Databend.Columns) {
		return empty, schemaddl.ErrConflict
	}
	source, target := qualified(r.Original.Target.Object), qualified(destination.Object)
	sql := "SELECT to_string(count(*)) AS __nyadb_copy_difference FROM " + target
	if r.WithData {
		var columns []string
		for _, c := range r.Original.Schema.Databend.Columns {
			columns = append(columns, quoteName(c.Name))
		}
		fields := strings.Join(columns, ", ")
		group := func(table string) string {
			return "SELECT " + fields + ", count(*) FROM " + table + " GROUP BY " + fields
		}
		left, right := group(source), group(target)
		// Full-row groups include their multiplicity. Equal counts plus DISTINCT
		// sets would incorrectly accept [1,1,2] copied as [1,2,2]. EXCEPT ALL is
		// not supported by the verified native server.
		sql = "SELECT to_string(count(*)) AS __nyadb_copy_difference FROM ((" + left + " EXCEPT " + right + ") UNION ALL (" + right + " EXCEPT " + left + ")) AS __nyadb_copy_diff"
	}
	q, err := d.s.query(ctx, session, sql, 2)
	if err != nil {
		return empty, err
	}
	if q.Truncated || len(q.Sets) != 1 || len(q.Sets[0].Columns) != 1 || q.Sets[0].Columns[0].Name != "__nyadb_copy_difference" || len(q.Sets[0].Rows) != 1 || len(q.Sets[0].Rows[0]) != 1 {
		return empty, contract.ErrUnsupported
	}
	cell := q.Sets[0].Rows[0][0]
	if cell.Kind != contract.Text || cell.Truncated || cell.Text != "0" {
		return empty, schemaddl.ErrDataChanged
	}
	current, err := d.s.readSchemaSnapshot(ctx, session, r.Original.Target, d.key[:])
	if err != nil {
		return empty, err
	}
	if !reflect.DeepEqual(current, r.Original) {
		return empty, schemaddl.ErrConflict
	}
	current, err = d.s.readSchemaSnapshot(ctx, session, destination, d.key[:])
	if err != nil {
		return empty, err
	}
	if !reflect.DeepEqual(current, actual) {
		return empty, schemaddl.ErrConflict
	}
	return actual, nil
}
