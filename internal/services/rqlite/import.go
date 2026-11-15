package rqlite

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"slices"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/importspec"
	"github.com/nyakang/nyadb/internal/services/connections"
)

// Executable writes are document-bound; the session publishes only the dialect
// and feature descriptor, so callers cannot bypass an original unknown write.
func (s *Session) DataImports() contract.DataImports { return contract.DataImports{Dialect: "sqlite"} }

func (d *dataDocument) importStream(ctx context.Context, id int64, db, name string, stream contract.DataImportStreamRequest) (out *contract.DataImportStreamResult, err error) {
	out = &contract.DataImportStreamResult{Incremental: true}
	r, err := importspec.Own(stream.Request)
	if err != nil {
		return out, err
	}
	if len(r.Edits) > 0 || stream.Rows < 0 || stream.Rows > int64(^uint(0)>>1) || stream.Open == nil || stream.RecordReceipt == nil || stream.StatementMaxBytes < 0 || stream.StatementMaxBytes > contract.MaxDataImportBytes {
		return out, contract.ErrDataPage
	}
	if stream.IgnoreForeignKeys || stream.CanonicalBackupValues {
		return out, contract.ErrUnsupported
	}
	if d.s.config.Options.ReadOnly {
		return out, contract.ErrDataReadOnly
	}
	if !validName(name) {
		return out, connections.ErrInvalid
	}
	if stream.Generation != 0 && stream.Generation != d.s.Generation() {
		return out, contract.ErrRevoked
	}
	child, done, err := d.admission(ctx, id, db, d.object.Name)
	if err != nil {
		return out, err
	}
	defer done()
	select {
	case <-child.Done():
		return out, child.Err()
	case d.writes <- struct{}{}:
	}
	defer func() { <-d.writes }()
	d.mu.Lock()
	blocked := d.unknown != nil
	d.mu.Unlock()
	if blocked {
		return out, contract.ErrDataCommitUncertain
	}
	object := contract.ObjectRef{ProfileID: id, Catalog: "main", Kind: "table", Name: name}
	t := tableSnapshot{}
	if r.NewTable {
		cols, e := importspec.Columns("sqlite", r.Columns)
		if e != nil {
			return out, e
		}
		version, e := d.s.request(child, "/db/query", []any{"SELECT schema_version FROM pragma_schema_version"}, true, false)
		if e != nil {
			return out, e
		}
		values, e := decodedRows(version, 0, 1)
		if e != nil || len(values) != 1 {
			return out, errIncomplete
		}
		t.version, e = cellInt(values[0][0])
		if e != nil {
			return out, e
		}
		t.structure = &contract.TableStructure{Object: object, Database: db, Name: name, Kind: "table", Columns: cols}
		var pk []string
		for _, col := range cols {
			if col.Key == "PRI" {
				pk = append(pk, col.Name)
			}
		}
		if len(pk) == 1 {
			for _, col := range cols {
				if col.Name == pk[0] && col.Type == "INTEGER" {
					t.rowIDKey = col.Name
				}
			}
		}
	} else {
		t, err = d.s.dataTable(child, id, db, name, object)
		if err != nil {
			return out, err
		}
	}
	key, err := importMatchKey(t, r)
	if err != nil {
		return out, err
	}
	if err = importspec.Validate("sqlite", t.structure.Columns, r); err != nil {
		return out, err
	}
	source, err := stream.Open(child)
	if err != nil {
		return out, err
	}
	if source == nil {
		return out, contract.ErrDataPage
	}
	closed := false
	defer func() {
		if !closed {
			err = errors.Join(err, source.Close())
		}
	}()
	first := true
	sequence := int64(0)
	pending := []importRow{}
	emit := func(rows []contract.DataImportRowResult) error {
		for _, row := range rows {
			out.Progress.Processed++
			switch row.Status {
			case "committed", "rolled_back", "uncertain":
				out.Progress.Prepared++
			case "skipped":
				out.Progress.Skipped++
			case "failed":
				out.Progress.Failed++
			}
			if len(out.Rows) < 1000 {
				out.Rows = append(out.Rows, row)
			}
			if stream.Record != nil {
				if e := stream.Record(row); e != nil {
					return e
				}
			}
		}
		if stream.Progress != nil {
			stream.Progress(out.Progress)
		}
		return nil
	}
	withheld := func() error {
		rows := []contract.DataImportRowResult{}
		for _, row := range pending {
			rows = append(rows, contract.DataImportRowResult{Database: db, Table: name, Row: int(row.number), Kind: r.Mode, Status: "not_processed"})
		}
		pending = nil
		return emit(rows)
	}
	flush := func() error {
		if len(pending) == 0 && !(first && (r.NewTable || r.Mode == "replace")) {
			return nil
		}
		batch, e := planImportBatch(t, r, pending, first)
		if e != nil {
			return errors.Join(e, withheld())
		}
		sequence++
		receipt, rows, version, e := executeImportBatch(d.s, child, t, r, batch, sequence)
		out.LastReceipt = &receipt
		if receipt.State == "committed" {
			out.CommittedRows += receipt.CommittedRows
			out.Changed = out.Changed || receipt.Changed
			out.Created = out.Created || receipt.Created
			t.version = version
			first = false
		}
		if receipt.State == "uncertain" {
			out.CommitUncertain = true
			out.CreationUncertain = batch.create
			d.mu.Lock()
			d.unknown = &contract.DataEditResult{CommitUncertain: true, Incomplete: true, NativeJSON: receipt.NativeJSON}
			d.mu.Unlock()
		}
		recordErr := stream.RecordReceipt(receipt)
		if recordErr == nil {
			out.ReceiptsRecorded++
		}
		pending = nil
		emitErr := emit(rows)
		if r.ContinueOnError && receipt.State == "rolled_back" && len(rows) == 1 && rows[0].Status == "failed" && !errors.Is(e, contract.ErrDataConflict) && child.Err() == nil {
			e = nil
		}
		return errors.Join(e, recordErr, emitErr)
	}
	seen := int64(0)
	for {
		if err = child.Err(); err != nil {
			return out, errors.Join(err, withheld())
		}
		edit, e := source.Next(child)
		if errors.Is(e, io.EOF) {
			break
		}
		if e != nil {
			return out, errors.Join(e, withheld())
		}
		if seen >= stream.Rows {
			return out, errors.Join(contract.ErrDataConflict, withheld())
		}
		seen++
		row, e := planImportRow(t, r, seen, edit, key)
		if e != nil {
			if err = withheld(); err != nil {
				return out, err
			}
			err = emit([]contract.DataImportRowResult{{Database: db, Table: name, Row: int(seen), Kind: "error", Status: "failed", Error: "row validation or database constraint failed"}})
			if err != nil {
				return out, err
			}
			if !r.ContinueOnError {
				return out, e
			}
			continue
		}
		trial := append(slices.Clone(pending), row)
		batch, e := planImportBatch(t, r, trial, first)
		tooLarge := false
		if e == nil && stream.StatementMaxBytes > 0 {
			for _, stmt := range batch.statements {
				if sql, ok := stmt.(string); ok && len(sql) > stream.StatementMaxBytes {
					tooLarge = true
				}
			}
		}
		if (e != nil || tooLarge) && len(pending) > 0 {
			if err = flush(); err != nil {
				return out, err
			}
			batch, e = planImportBatch(t, r, []importRow{row}, first)
			tooLarge = false
			if e == nil && stream.StatementMaxBytes > 0 {
				for _, stmt := range batch.statements {
					if sql, ok := stmt.(string); ok && len(sql) > stream.StatementMaxBytes {
						tooLarge = true
					}
				}
			}
		}
		if e != nil || tooLarge {
			pending = append(pending, row)
			return out, errors.Join(contract.ErrDataEditLimit, withheld())
		}
		pending = append(pending, row)
		if r.ContinueOnError || len(pending) >= 64 {
			if err = flush(); err != nil {
				return out, err
			}
		}
	}
	if seen != stream.Rows {
		return out, errors.Join(contract.ErrDataConflict, withheld())
	}
	closed = true
	if err = source.Close(); err != nil {
		return out, errors.Join(err, withheld())
	}
	if err = flush(); err != nil {
		return out, err
	}
	out.Committed = true
	return out, nil
}

type importMemoryRows struct {
	rows []contract.DataEdit
	next int
}

func (r *importMemoryRows) Next(ctx context.Context) (contract.DataEdit, error) {
	if err := ctx.Err(); err != nil {
		return contract.DataEdit{}, err
	}
	if r.next == len(r.rows) {
		return contract.DataEdit{}, io.EOF
	}
	e := r.rows[r.next]
	r.next++
	return e, nil
}
func (r *importMemoryRows) Close() error { return nil }

func (d *dataDocument) importData(ctx context.Context, id int64, db, name string, r contract.DataImportRequest) (*contract.DataImportResult, error) {
	r, err := importspec.Own(r)
	if err != nil {
		return nil, err
	}
	edits := r.Edits
	r.Edits = nil
	receipts := []contract.DataImportBatchReceipt{}
	bytes := 0
	rows := []contract.DataImportRowResult{}
	out, err := d.importStream(ctx, id, db, name, contract.DataImportStreamRequest{Request: r, Rows: int64(len(edits)), Open: func(context.Context) (contract.DataImportRowSource, error) {
		return &importMemoryRows{rows: edits}, nil
	}, Record: func(row contract.DataImportRowResult) error { rows = append(rows, row); return nil }, RecordReceipt: func(receipt contract.DataImportBatchReceipt) error {
		receipts = append(receipts, receipt)
		bytes += len(receipt.NativeJSON)
		if bytes >= maxReceiptBytes {
			return contract.ErrDataEditLimit
		}
		return nil
	}})
	if out == nil {
		return nil, err
	}
	saved := &contract.DataEditResult{Applied: int(out.CommittedRows), CommitConfirmed: out.Committed, CommitUncertain: out.CommitUncertain, RefreshRequired: out.Changed}
	for _, row := range rows {
		if row.Outcome != nil {
			saved.Outcomes = append(saved.Outcomes, *row.Outcome)
		}
	}
	if len(receipts) == 1 {
		saved.NativeJSON = receipts[0].NativeJSON
	} else if len(receipts) > 1 {
		raw, e := json.Marshal(receipts)
		if e != nil {
			return nil, errors.Join(err, e)
		}
		saved.NativeJSON = string(raw)
	}
	return &contract.DataImportResult{Incremental: out.Incremental, Changed: out.Changed, CommittedRows: out.CommittedRows, Created: out.Created, CreationUncertain: out.CreationUncertain, Committed: out.Committed, CommitUncertain: out.CommitUncertain, Rows: rows, Saved: saved, Receipts: receipts}, err
}

func (d *dataDocument) importNewTable(ctx context.Context, id int64, db, name string, cols []contract.DataImportColumn, edits []contract.DataEdit) (*contract.DataNewTableImportResult, error) {
	r, e := d.importData(ctx, id, db, name, contract.DataImportRequest{NewTable: true, Columns: cols, Edits: edits})
	if r == nil {
		return nil, e
	}
	return &contract.DataNewTableImportResult{Created: r.Created, CreationUncertain: r.CreationUncertain, Saved: r.Saved}, e
}

var _ contract.DataImportsProvider = (*Session)(nil)
