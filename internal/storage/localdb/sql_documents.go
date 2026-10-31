package localdb

import (
	"context"
	"database/sql"
	"errors"
	"github.com/nyakang/nyadb/internal/services/queries"
)

type SQLRepository struct{ db *sql.DB }

func NewSQLRepository(db *sql.DB) *SQLRepository { return &SQLRepository{db: db} }
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
