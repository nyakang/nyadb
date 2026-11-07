package dameng

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"encoding/binary"
	"errors"
	"hash"
	"io"
	"slices"
	"sort"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/importspec"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/dameng/dpi"
)

func (s *Session) DataImports() contract.DataImports {
	return contract.DataImports{Dialect: "dameng", Importer: s.importRows, StreamImporter: s.importStream, NewTableImporter: s.importNewTable}
}

type importRowsSource struct {
	edits []contract.DataEdit
	index int
}

func (r *importRowsSource) Close() error { return nil }
func (r *importRowsSource) Next(ctx context.Context) (contract.DataEdit, error) {
	if err := ctx.Err(); err != nil {
		return contract.DataEdit{}, err
	}
	if r.index >= len(r.edits) {
		return contract.DataEdit{}, io.EOF
	}
	e := r.edits[r.index]
	r.index++
	return e, nil
}

func cloneImportOutcome(v *contract.DataEditOutcome) *contract.DataEditOutcome {
	if v == nil {
		return nil
	}
	out := *v
	out.Original = cloneDataSnapshot(v.Original)
	out.Saved = cloneDataSnapshot(v.Saved)
	out.PreviewColumns = slices.Clone(v.PreviewColumns)
	out.Previews = make([]contract.DataCell, len(v.Previews))
	for i, c := range v.Previews {
		out.Previews[i] = c
		if c.Value != nil {
			v := c.Value.Clone()
			out.Previews[i].Value = &v
		}
	}
	return &out
}

func (s *Session) importRows(ctx context.Context, id int64, schema, name string, r contract.DataImportRequest) (*contract.DataImportResult, error) {
	r, err := importspec.Own(r)
	if err != nil {
		return nil, err
	}
	edits := r.Edits
	r.Edits = nil
	stream := contract.DataImportStreamRequest{Request: r, Rows: int64(len(edits)), Open: func(context.Context) (contract.DataImportRowSource, error) {
		return &importRowsSource{edits: edits}, nil
	}}
	out, err := s.runImport(ctx, id, schema, name, stream, true)
	if out == nil {
		return nil, err
	}
	result := &contract.DataImportResult{Created: out.Created, CreationUncertain: out.CreationUncertain, Committed: out.Committed, CommitUncertain: out.CommitUncertain}
	result.Rows = make([]contract.DataImportRowResult, len(edits))
	for i := range result.Rows {
		result.Rows[i] = contract.DataImportRowResult{Database: schema, Table: name, Row: i + 1, Status: "not_processed"}
	}
	for _, row := range out.Rows {
		if row.Row > 0 && row.Row <= len(result.Rows) {
			result.Rows[row.Row-1] = row
		}
	}
	if out.Committed || out.CommitUncertain {
		result.Saved = &contract.DataEditResult{Applied: int(out.Progress.Prepared), CommitConfirmed: out.Committed, CommitUncertain: out.CommitUncertain, Incomplete: out.CommitUncertain && !errors.Is(err, dpi.ErrOutcomeUnknown)}
		for _, row := range out.Rows {
			if out.CommitUncertain && row.Error != "" {
				result.Saved.Incomplete = true
			}
			if row.Outcome != nil && (row.Status == "committed" || row.Status == "uncertain") {
				v := cloneImportOutcome(row.Outcome)
				result.Saved.Outcomes = append(result.Saved.Outcomes, *v)
				result.Saved.Saved = append(result.Saved.Saved, savedEdit(contract.DataEdit{Kind: v.Kind, Original: v.Original}, v.Saved))
			}
		}
	}
	return result, err
}

func (s *Session) importNewTable(ctx context.Context, id int64, schema, name string, columns []contract.DataImportColumn, edits []contract.DataEdit) (*contract.DataNewTableImportResult, error) {
	out, err := s.importRows(ctx, id, schema, name, contract.DataImportRequest{NewTable: true, Columns: columns, Edits: edits})
	if out == nil {
		return nil, err
	}
	return &contract.DataNewTableImportResult{Created: out.Created, CreationUncertain: out.CreationUncertain, Saved: out.Saved}, err
}

func (s *Session) importStream(ctx context.Context, id int64, schema, name string, stream contract.DataImportStreamRequest) (*contract.DataImportStreamResult, error) {
	return s.runImport(ctx, id, schema, name, stream, false)
}

// Hash the immutable reviewed input with length-delimited values, independently
// of map iteration order and without retaining or JSON-encoding the full source.
func hashImportRow(h hash.Hash, e contract.DataEdit) {
	write := func(data []byte) {
		var size [8]byte
		binary.LittleEndian.PutUint64(size[:], uint64(len(data)))
		h.Write(size[:])
		h.Write(data)
	}
	names := make([]string, 0, len(e.Values))
	for name := range e.Values {
		names = append(names, name)
	}
	sort.Strings(names)
	var count [8]byte
	binary.LittleEndian.PutUint64(count[:], uint64(len(names)))
	h.Write(count[:])
	for _, name := range names {
		v := e.Values[name]
		write([]byte(name))
		write([]byte(v.Kind))
		write([]byte(v.Text))
		write(v.Bytes)
		flags := byte(0)
		if v.Null {
			flags |= 1
		}
		if v.Default {
			flags |= 2
		}
		if v.Binary {
			flags |= 4
		}
		h.Write([]byte{flags})
	}
}

func ownedImportRow(e contract.DataEdit) (contract.DataEdit, error) {
	r, err := importspec.Own(contract.DataImportRequest{Edits: []contract.DataEdit{e}})
	if err != nil {
		return contract.DataEdit{}, err
	}
	return r.Edits[0], nil
}

func preflightImport(ctx context.Context, t *contract.TableStructure, r contract.DataImportRequest, stream contract.DataImportStreamRequest) (digest []byte, err error) {
	source, err := stream.Open(ctx)
	if err != nil {
		return nil, err
	}
	if source == nil {
		return nil, connections.ErrInvalid
	}
	defer func() { err = errors.Join(err, source.Close()) }()
	h := sha256.New()
	var rows, valid int64
	for {
		if err = ctx.Err(); err != nil {
			return nil, err
		}
		e, eof := source.Next(ctx)
		if eof == io.EOF {
			break
		}
		if eof != nil {
			return nil, eof
		}
		rows++
		if rows > stream.Rows {
			return nil, connections.ErrInvalid
		}
		e, err = ownedImportRow(e)
		if err != nil {
			return nil, err
		}
		hashImportRow(h, e)
		if err = validateImportRow(t, r, e); err != nil {
			if !r.ContinueOnError || errors.Is(err, contract.ErrDataReadOnly) || errors.Is(err, contract.ErrDataPage) {
				return nil, err
			}
		} else {
			valid++
		}
	}
	if rows != stream.Rows || valid == 0 {
		return nil, connections.ErrInvalid
	}
	return h.Sum(nil), nil
}

// Every request owns one original physical connection in the bounded data
// pool. Source/driver/cursor/rollback acknowledgement precedes publication.
func (s *Session) runImport(ctx context.Context, id int64, schema, name string, stream contract.DataImportStreamRequest, details bool) (out *contract.DataImportStreamResult, err error) {
	out = &contract.DataImportStreamResult{}
	o, err := s.dataObject(id, schema, name, contract.ObjectRef{})
	if err != nil {
		return out, err
	}
	if stream.Generation != 0 && stream.Generation != s.Generation() {
		return out, contract.ErrRevoked
	}
	if stream.Open == nil || stream.Rows <= 0 || stream.StatementMaxBytes < 0 || stream.StatementMaxBytes > contract.MaxDataImportBytes || len(stream.Request.Edits) != 0 {
		return out, connections.ErrInvalid
	}
	if stream.IgnoreForeignKeys || stream.CanonicalBackupValues {
		return out, contract.ErrUnsupported
	}
	r, err := importspec.Own(stream.Request)
	if err != nil {
		return out, err
	}
	ctx, done, err := s.request(ctx)
	if err != nil {
		return out, err
	}
	defer done()
	c, err := s.dataDB.Conn(ctx)
	if err != nil {
		return out, err
	}
	var tx dpi.ContextTransaction
	mutated, commitSubmitted := false, false
	defer func() {
		if tx != nil && !out.Committed {
			rb := tx.Rollback()
			if rb != nil {
				err = errors.Join(err, rb)
				if mutated {
					out.CommitUncertain = true
				}
			}
		}
		if commitSubmitted && !out.Committed {
			out.CommitUncertain = true
		}
		if out.CommitUncertain {
			err = errors.Join(err, contract.ErrDataCommitUncertain)
		}
		for i := range out.Rows {
			if out.Rows[i].Status == "uncertain" && !out.CommitUncertain && !out.Committed {
				// A pre-submission cancellation can leave the original transaction
				// usable. Its acknowledged rollback settles even attempted rows.
				out.Rows[i].Status = "rolled_back"
			}
			if out.Rows[i].Status == "prepared" {
				switch {
				case out.Committed:
					out.Rows[i].Status = "committed"
				case out.CommitUncertain:
					out.Rows[i].Status = "uncertain"
				default:
					out.Rows[i].Status = "rolled_back"
				}
			}
		}
		_ = c.Raw(func(any) error { return driver.ErrBadConn })
		if e := c.Close(); !errors.Is(e, sql.ErrConnDone) {
			err = errors.Join(err, e)
		}
	}()
	begin := func() error {
		e := c.Raw(func(raw any) error {
			b, ok := raw.(driver.ConnBeginTx)
			if !ok {
				return contract.ErrUnsupported
			}
			v, e := b.BeginTx(ctx, driver.TxOptions{})
			if e != nil {
				return e
			}
			tx, ok = v.(dpi.ContextTransaction)
			if !ok {
				return errors.Join(contract.ErrUnsupported, v.Rollback())
			}
			return nil
		})
		if e != nil {
			return e
		}
		for _, sql := range []string{"SET TRANSACTION ISOLATION LEVEL SERIALIZABLE", "LOCK TABLE " + qualified(o) + " IN EXCLUSIVE MODE"} {
			if _, e = c.ExecContext(ctx, sql); e != nil {
				return e
			}
		}
		return nil
	}
	var structure *contract.TableStructure
	var ddl string
	if r.NewTable {
		columns, e := importspec.Columns("dameng", r.Columns)
		if e != nil {
			return out, e
		}
		ddl, err = importspec.DDL("dameng", schema, name, r.Columns)
		if err != nil {
			return out, err
		}
		structure = &contract.TableStructure{Object: o, Columns: columns}
	} else {
		if err = begin(); err != nil {
			return out, err
		}
		structure, err = structureAt(ctx, c, o)
		if err != nil {
			return out, err
		}
		if err = importTableAt(ctx, c, structure); err != nil {
			return out, err
		}
	}
	var key []string
	if importspec.NeedsKey(r.Mode) {
		key, err = importKeyAt(ctx, c, structure, r.MatchColumns)
		if err != nil {
			return out, err
		}
	}
	check := r
	check.NewTable = false
	check.Columns = nil
	if err = importspec.Validate("dameng", structure.Columns, check); err != nil {
		return out, err
	}
	digest, err := preflightImport(ctx, structure, r, stream)
	if err != nil {
		return out, err
	}
	if r.NewTable {
		if stream.StatementMaxBytes > 0 && len(ddl) > stream.StatementMaxBytes {
			return out, contract.ErrDataEditLimit
		}
		if err = ctx.Err(); err != nil {
			return out, err
		}
		if _, err = c.ExecContext(ctx, ddl); err != nil {
			out.CreationUncertain = errors.Is(err, dpi.ErrOutcomeUnknown)
			return out, err
		}
		out.Created = true
		if err = begin(); err != nil {
			return out, err
		}
		structure, err = structureAt(ctx, c, o)
		if err != nil {
			return out, err
		}
		if err = importTableAt(ctx, c, structure); err != nil {
			return out, err
		}
	}
	source, err := stream.Open(ctx)
	if err != nil {
		return out, err
	}
	if source == nil {
		return out, connections.ErrInvalid
	}
	closed := false
	defer func() {
		if !closed {
			err = errors.Join(err, source.Close())
		}
	}()
	if r.Mode == "replace" {
		if err = importExec(ctx, c, "DELETE FROM "+qualified(o), nil, stream.StatementMaxBytes, &mutated); err != nil {
			return out, err
		}
	}
	h := sha256.New()
	budget := 16 << 20
	if details {
		budget = dataBatchBytes / 2
	}
	for {
		if err = ctx.Err(); err != nil {
			return out, err
		}
		input, e := source.Next(ctx)
		if e == io.EOF {
			break
		}
		if e != nil {
			return out, e
		}
		if out.Progress.Processed >= stream.Rows {
			return out, connections.ErrInvalid
		}
		input, e = ownedImportRow(input)
		if e != nil {
			return out, e
		}
		hashImportRow(h, input)
		row := contract.DataImportRowResult{Row: int(out.Progress.Processed + 1), Database: schema, Table: name}
		if _, err = c.ExecContext(ctx, "SAVEPOINT NYADB_IMPORT_ROW"); err != nil {
			return out, err
		}
		e = validateImportRow(structure, r, input)
		rowMutated := false
		if e == nil {
			row, e = s.importRow(ctx, c, structure, r, key, input, stream.StatementMaxBytes, &rowMutated)
			row.Row = int(out.Progress.Processed + 1)
			row.Database, row.Table = schema, name
		}
		mutated = mutated || rowMutated
		out.Progress.Processed++
		if e != nil {
			row.Kind, row.Status, row.Error = "error", "failed", importRowDiagnostic(e)
			out.Progress.Failed++
			if rowMutated && connectionLost(e) {
				row.Status = "uncertain"
			}
			if r.ContinueOnError && ctx.Err() == nil && !connectionLost(e) && !errors.Is(e, contract.ErrDataReadOnly) && !errors.Is(e, contract.ErrDataPage) {
				cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
				_, rb := c.ExecContext(cleanup, "ROLLBACK TO SAVEPOINT NYADB_IMPORT_ROW")
				stop()
				if rb == nil {
					e = nil
				} else {
					e = errors.Join(e, rb)
				}
			}
		} else if row.Kind == "skip" {
			out.Progress.Skipped++
		} else {
			out.Progress.Prepared++
		}
		if !details && row.Outcome != nil {
			row.Outcome = &contract.DataEditOutcome{Kind: row.Outcome.Kind, GeneratedID: row.Outcome.GeneratedID, PreviewColumns: row.Outcome.PreviewColumns, Previews: row.Outcome.Previews}
		}
		retained := row
		if details && row.Outcome != nil {
			if row.Outcome.Original != nil {
				err = chargeDataRow(&budget, row.Outcome.Original.Original)
			}
			if err == nil && row.Outcome.Saved != nil {
				err = chargeDataRow(&budget, row.Outcome.Saved.Original)
			}
			if err != nil {
				return out, err
			}
		} else if row.Outcome != nil {
			size := 0
			for _, p := range row.Outcome.Previews {
				size += 192 + len(p.Text)
				if p.Value != nil {
					size += len(p.Value.Text) + len(p.Value.Bytes)*3
				}
			}
			if size > budget {
				retained.Outcome = nil
			} else {
				budget -= size
			}
		}
		if details || len(out.Rows) < 1000 {
			retained.Outcome = cloneImportOutcome(retained.Outcome)
			out.Rows = append(out.Rows, retained)
		}
		if stream.Record != nil {
			record := row
			record.Outcome = cloneImportOutcome(row.Outcome)
			if recordErr := stream.Record(record); recordErr != nil {
				return out, recordErr
			}
		}
		if stream.Progress != nil {
			stream.Progress(out.Progress)
		}
		if e != nil {
			return out, e
		}
	}
	if out.Progress.Processed != stream.Rows || !slices.Equal(h.Sum(nil), digest) {
		return out, connections.ErrInvalid
	}
	closed = true
	if err = source.Close(); err != nil {
		return out, err
	}
	if err = ctx.Err(); err != nil {
		return out, err
	}
	err = tx.CommitContext(ctx)
	commitSubmitted = err == nil || errors.Is(err, dpi.ErrOutcomeUnknown) || !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded)
	if err != nil {
		return out, err
	}
	out.Committed = true
	tx = nil
	return out, nil
}

var _ contract.DataImportsProvider = (*Session)(nil)
