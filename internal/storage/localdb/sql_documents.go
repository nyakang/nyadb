package localdb

import (
	"context"
	"database/sql"
	"errors"
	"github.com/nyakang/nyadb/internal/services/queries"
)

type SQLRepository struct{ db *sql.DB }

func NewSQLRepository(db *sql.DB) *SQLRepository { return &SQLRepository{db: db} }

func (s *SQLRepository) GetRecord(ctx context.Context, kind, id string) (queries.Record, bool, error) {
	var r queries.Record
	err := s.db.QueryRowContext(ctx, "SELECT id,kind,profile_id,format_version,revision,payload FROM sql_documents WHERE id=? AND kind=?", id, kind).Scan(&r.ID, &r.Kind, &r.ProfileID, &r.Version, &r.Revision, &r.Payload)
	if errors.Is(err, sql.ErrNoRows) {
		return r, false, nil
	}
	return r, err == nil, err
}

func (s *SQLRepository) DeleteInitialSQL(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM sql_documents WHERE id=? AND kind='initial_sql'", id)
	return err
}
func (s *SQLRepository) List(ctx context.Context, kind string, profile int64) ([]queries.Record, error) {
	rows, e := s.db.QueryContext(ctx, "SELECT id,kind,profile_id,format_version,revision,payload FROM sql_documents WHERE kind=? AND (?=0 OR profile_id=?) ORDER BY id", kind, profile, profile)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	var out []queries.Record
	for rows.Next() {
		var r queries.Record
		if e = rows.Scan(&r.ID, &r.Kind, &r.ProfileID, &r.Version, &r.Revision, &r.Payload); e != nil {
			return nil, e
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
func (s *SQLRepository) Put(ctx context.Context, r queries.Record) error {
	if r.ID == "" || r.Version != 1 || len(r.Payload) == 0 || r.Revision == 0 {
		return errors.New("invalid encrypted SQL record")
	}
	v, e := s.db.ExecContext(ctx, `INSERT INTO sql_documents(id,kind,profile_id,format_version,revision,payload) VALUES(?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET kind=excluded.kind,profile_id=excluded.profile_id,format_version=excluded.format_version,revision=excluded.revision,payload=excluded.payload WHERE excluded.revision>sql_documents.revision`, r.ID, r.Kind, r.ProfileID, r.Version, r.Revision, r.Payload)
	if e != nil {
		return e
	}
	n, e := v.RowsAffected()
	if e == nil && n == 0 {
		return queries.ErrStaleRevision
	}
	return e
}
func (s *SQLRepository) Delete(ctx context.Context, id string) error {
	_, e := s.db.ExecContext(ctx, "DELETE FROM sql_documents WHERE id=? AND kind='query'", id)
	return e
}
func (s *SQLRepository) HasRecords(ctx context.Context) (bool, error) {
	var n int
	e := s.db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM sql_documents)").Scan(&n)
	return n != 0, e
}

// PutConditional makes deletion and revision conflicts atomic across Store instances.
func (s *SQLRepository) PutConditional(ctx context.Context, r queries.Record, expected uint64) error {
	if r.ID == "" || r.Version != 1 || len(r.Payload) == 0 || r.Revision != expected+1 {
		return errors.New("invalid conditional SQL record")
	}
	var v sql.Result
	var e error
	if expected == 0 {
		v, e = s.db.ExecContext(ctx, `INSERT INTO sql_documents(id,kind,profile_id,format_version,revision,payload) VALUES(?,?,?,?,?,?) ON CONFLICT(id) DO NOTHING`, r.ID, r.Kind, r.ProfileID, r.Version, r.Revision, r.Payload)
	} else {
		source := r.Kind
		if source == "named_workspace_deleted" {
			source = "named_workspace"
		}
		if source == "query_deleted" {
			source = "query"
		}
		v, e = s.db.ExecContext(ctx, `UPDATE sql_documents SET kind=?,format_version=?,revision=?,payload=? WHERE id=? AND profile_id=? AND kind=? AND revision=?`, r.Kind, r.Version, r.Revision, r.Payload, r.ID, r.ProfileID, source, expected)
	}
	if e != nil {
		return e
	}
	n, e := v.RowsAffected()
	if e == nil && n != 1 {
		return queries.ErrStaleRevision
	}
	return e
}
