package oracle

import (
	"context"
	"database/sql"
	"errors"
	"io"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func (s *Session) DataImports() contract.DataImports {
	return contract.DataImports{Dialect: "oracle", Importer: s.importRows, StreamImporter: s.importStream}
}
func validImport(r contract.DataImportRequest) error {
	if r.NewTable || r.ContinueOnError || len(r.MatchColumns) > 0 || len(r.Columns) > 0 || r.Mode != "" && r.Mode != "append" {
		return contract.ErrUnsupported
	}
	return nil
}
func (s *Session) importRows(ctx context.Context, id int64, schema, name string, r contract.DataImportRequest) (*contract.DataImportResult, error) {
	if err := validImport(r); err != nil {
		return nil, err
	}
	for _, e := range r.Edits {
		if e.Kind != "insert" || e.Original != nil {
			return nil, contract.ErrUnsupported
		}
	}
	saved, err := s.save(ctx, id, schema, name, r.Edits)
	result := &contract.DataImportResult{Saved: saved}
	if saved != nil {
		result.Committed = saved.CommitConfirmed
		result.CommitUncertain = saved.CommitUncertain
	}
	for i, e := range r.Edits {
		status := "rolled_back"
		if result.Committed {
			status = "committed"
		}
		if result.CommitUncertain {
			status = "uncertain"
		}
		result.Rows = append(result.Rows, contract.DataImportRowResult{Database: schema, Table: name, Row: i + 1, Kind: e.Kind, Status: status})
	}
	return result, err
}
func (s *Session) importStream(ctx context.Context, id int64, schema, name string, r contract.DataImportStreamRequest) (*contract.DataImportStreamResult, error) {
	if r.Generation != 0 && r.Generation != s.Generation() {
		return nil, contract.ErrRevoked
	}
	if err := validImport(r.Request); err != nil {
		return nil, err
	}
	if r.Open == nil || r.IgnoreForeignKeys || r.ExtendedInsert || r.Rows < 0 || r.Rows > contract.MaxDataImportRows {
		return nil, contract.ErrUnsupported
	}
	child, done, err := s.request(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	source, err := r.Open(child)
	if err != nil {
		return nil, err
	}
	defer source.Close()
	tx, err := s.db.BeginTx(child, &sql.TxOptions{Isolation: sql.LevelDefault})
	if err != nil {
		return nil, safeError(err)
	}
	defer tx.Rollback()
	result := &contract.DataImportStreamResult{}
	for {
		e, err := source.Next(child)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return result, err
		}
		if e.Kind != "insert" || e.Original != nil {
			return result, contract.ErrUnsupported
		}
		result.Progress.Processed++
		if result.Progress.Processed > contract.MaxDataImportRows {
			return result, contract.ErrDataEditLimit
		}
		if _, err = s.apply(child, tx, id, schema, name, []contract.DataEdit{e}); err != nil {
			return result, err
		}
		result.Progress.Prepared++
		row := contract.DataImportRowResult{Database: schema, Table: name, Row: int(result.Progress.Processed), Kind: "insert", Status: "prepared"}
		if len(result.Rows) < 100 {
			result.Rows = append(result.Rows, row)
		}
		if r.Record != nil {
			if err = r.Record(row); err != nil {
				return result, err
			}
		}
		if r.Progress != nil {
			r.Progress(result.Progress)
		}
	}
	if r.Rows != 0 && result.Progress.Processed != r.Rows {
		return result, contract.ErrDataConflict
	}
	if err = tx.Commit(); err != nil {
		result.CommitUncertain = true
		return result, errors.Join(contract.ErrDataCommitUncertain, safeError(err))
	}
	result.Committed = true
	for i := range result.Rows {
		result.Rows[i].Status = "committed"
	}
	return result, nil
}
