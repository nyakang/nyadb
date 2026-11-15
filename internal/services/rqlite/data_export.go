package rqlite

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

var errExportBatch = errors.New("RQLite export batch exceeds its complete-value bound")
var errExportValue = errors.New("RQLite export exceeds the 1 MiB field or 16 MiB row limit")

func exportProjection(t tableSnapshot, source string, fields []string) string {
	var ordinals, cases []string
	for i, field := range fields {
		ordinals = append(ordinals, fmt.Sprintf("(%d)", i))
		cases = append(cases, fmt.Sprintf("WHEN %d THEN r.c%d", i, slices.Index(columnNames(t), field)))
	}
	v := "CASE c.i " + strings.Join(cases, " ") + " END"
	return "WITH _nyadb_export_rows AS MATERIALIZED (SELECT row_number() OVER ()-1 AS rid,* FROM (" + source + ")),_nyadb_export_columns(i) AS (VALUES " + strings.Join(ordinals, ",") + ")," +
		"_nyadb_export_cells AS MATERIALIZED (SELECT r.rid,c.i AS cid,typeof(" + v + ") AS typ," + dataBytes(v) + " AS v FROM _nyadb_export_rows r CROSS JOIN _nyadb_export_columns c)" +
		fmt.Sprintf(" SELECT rid,cid,typ,CASE WHEN coalesce(length(v),0)<=%d AND sum(coalesce(length(v),0)) OVER (PARTITION BY rid)<=%d AND sum(coalesce(length(v),0)) OVER ()<=%d THEN hex(v) ELSE NULL END,length(v) FROM _nyadb_export_cells ORDER BY rid,cid", contract.DataExportFieldMaxBytes, contract.DataExportRowMaxBytes, contract.DataExportRowMaxBytes)
}

func decodeExportRows(r receipt, fields, limit int) ([][]contract.DataValue, error) {
	rows, err := decodedRows(r, 0, 5)
	if err != nil || fields == 0 || len(rows)%fields != 0 || len(rows) > fields*limit {
		return nil, errIncomplete
	}
	out := make([][]contract.DataValue, len(rows)/fields)
	for i := range out {
		out[i] = make([]contract.DataValue, fields)
	}
	rowBytes := make([]int, len(out))
	batchBound, completeValue := false, false
	batchBytes := int64(0)
	for i, row := range rows {
		rid, err := cellInt(row[0])
		cid, e := cellInt(row[1])
		if err != nil || e != nil || rid != int64(i/fields) || cid != int64(i%fields) {
			return nil, errIncomplete
		}
		typ, err := cellText(row[2])
		if err != nil {
			return nil, errIncomplete
		}
		if typ == "null" {
			if row[4].Kind != contract.Null || row[3].Kind != contract.Null && row[3].Text != "" {
				return nil, errIncomplete
			}
			out[rid][cid] = contract.DataValue{Kind: contract.Null, Null: true}
			continue
		}
		n, err := cellInt(row[4])
		if err != nil || n < 0 {
			return nil, errIncomplete
		}
		if n > contract.DataExportFieldMaxBytes {
			return nil, errExportValue
		}
		rowBytes[rid] += int(n)
		batchBytes += n
		if rowBytes[rid] > contract.DataExportRowMaxBytes {
			return nil, errExportValue
		}
		if row[3].Kind == contract.Null {
			batchBound = true
			continue
		}
		completeValue = true
		raw, err := cellText(row[3])
		if err != nil || int64(len(raw)) != n*2 {
			return nil, errIncomplete
		}
		b, err := hex.DecodeString(raw)
		if err != nil {
			return nil, errIncomplete
		}
		v, err := storageDataCell(typ, b, false)
		if err != nil {
			return nil, err
		}
		out[rid][cid] = contract.DataValue{Kind: v.Kind, Text: v.Text, Bytes: v.Bytes, Binary: v.Kind == contract.Binary}
	}
	if batchBound {
		if batchBytes <= contract.DataExportRowMaxBytes || completeValue {
			return nil, errIncomplete
		}
		return nil, errExportBatch
	}
	if batchBytes > contract.DataExportRowMaxBytes {
		return nil, errIncomplete
	}
	return out, nil
}

// Each bounded native batch is a complete read transaction. The whole export
// remains a live read; no HTTP transaction is held between batches or sinks.
func (s *Session) exportData(ctx context.Context, id int64, db, table string, o contract.DataOptions, fields []string, sink func([]contract.DataValue) error) error {
	if sink == nil || len(fields) == 0 || len(fields) > maxDataColumns || o.After != nil || o.UseKeyset || o.Offset < 0 || o.Limit < 0 || o.Limit > 1000 {
		return connections.ErrInvalid
	}
	t, err := s.dataTable(ctx, id, db, table, o.Object)
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, field := range fields {
		if seen[field] || !slices.Contains(columnNames(t), field) {
			return connections.ErrInvalid
		}
		seen[field] = true
	}
	order, err := dataOrder(t, o)
	if err != nil {
		return err
	}
	base, err := dataOrderedSelect(t, o, false, order, "")
	if err != nil {
		return err
	}
	offset, batch, written := o.Offset, 32, 0
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		size := batch
		if o.Limit > 0 {
			size = min(size, o.Limit-written)
			if size <= 0 {
				return nil
			}
		}
		source := base + fmt.Sprintf(" LIMIT %d OFFSET %d", size, offset)
		// Hex encoding of a complete 16 MiB row needs slightly over 32 MiB.
		// Only this bounded read can admit a 64 MiB receipt; writes retain 32 MiB.
		r, err := s.requestBounded(ctx, "/db/query", []any{"SELECT " + schemaGuard(t), exportProjection(t, source, fields)}, true, false, 64<<20)
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		guard, err := decodedRows(r, 0, 1)
		if err != nil || len(guard) != 1 || guard[0][0].Text != "1" {
			return contract.ErrDataConflict
		}
		r.Results = r.Results[1:]
		rows, err := decodeExportRows(r, len(fields), size)
		if errors.Is(err, errExportBatch) && size > 1 {
			batch = max(1, size/2)
			continue
		}
		if err != nil {
			return err
		}
		for _, row := range rows {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := sink(row); err != nil {
				return err
			}
		}
		written += len(rows)
		offset += int64(len(rows))
		if len(rows) < size {
			return nil
		}
	}
}

func (s *Session) exportJSONL(ctx context.Context, o contract.ObjectRef, w io.Writer) error {
	if w == nil {
		return connections.ErrInvalid
	}
	t, err := s.dataTable(ctx, o.ProfileID, "main", o.Name, o)
	if err != nil {
		return err
	}
	var columns []contract.Column
	for _, c := range t.structure.Columns {
		columns = append(columns, contract.Column{Name: c.Name, DatabaseType: c.Type, Nullable: c.Nullable})
	}
	encoder := json.NewEncoder(w)
	if err := encoder.Encode(struct {
		Version int
		Columns []contract.Column
	}{1, columns}); err != nil {
		return err
	}
	return s.exportData(ctx, o.ProfileID, "main", o.Name, contract.DataOptions{Object: o}, columnNames(t), func(row []contract.DataValue) error {
		cells := make([]contract.Cell, len(row))
		for i, v := range row {
			cells[i] = contract.Cell{Kind: v.Kind, Text: v.Text, Bytes: v.Bytes}
		}
		return encoder.Encode(cells)
	})
}
