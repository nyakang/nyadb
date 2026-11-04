package engines

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"

	"github.com/nyakang/nyadb/internal/db/contract"
	sqlitedriver "modernc.org/sqlite"
)

func (s *Session) sqliteBackupPlan(ctx context.Context, r contract.BackupRequest) (contract.BackupPlan, error) {
	p := contract.BackupPlan{Request: r, Engine: "sqlite"}
	if len(r.Tables) > 0 || r.PreserveACL || r.PreserveOwner {
		return p, contract.ErrUnsupported
	}
	d := s.strategy.(sqliteDialect)
	source := s.catalog
	if r.Target.Object.Schema != "main" {
		source = ""
		for _, a := range d.options.Attachments {
			if a.Name == r.Target.Object.Schema {
				source = a.File
			}
		}
	}
	if source == "" {
		return p, contract.ErrUnsupported
	}
	source, err := filepath.Abs(source)
	if err != nil {
		return p, err
	}
	p.Source = source
	p.Objects = []string{source}
	p.Warnings = []string{"SQLite 备份只包含当前选择的一个数据库文件。"}
	if r.Action == "restore" {
		if sameBackupFile(source, r.Path) {
			return p, errors.New("restore source cannot be the live target file")
		}
		if err = sqliteBackupIntegrity(ctx, r.Path); err != nil {
			return p, err
		}
		if p.Digest, err = backupDigest(ctx, r.Path); err != nil {
			return p, err
		}
		if r.Overwrite {
			if !sameBackupFile(source, r.Destination) {
				return p, errors.New("overwrite must target the selected physical file")
			}
			p.QuiesceFile = source
		} else if _, err = os.Lstat(r.Destination); !os.IsNotExist(err) {
			return p, errors.New("new restore destination already exists or cannot be checked")
		}
	} else if sameBackupFile(source, r.Path) {
		return p, errors.New("backup destination must differ from live file")
	}
	return p, nil
}
func sqliteBackupURI(path, mode string) string {
	u, _ := url.Parse(sqliteFileURI(filepath.ToSlash(path)))
	q := u.Query()
	q.Set("mode", mode)
	u.RawQuery = q.Encode()
	return u.String()
}
func sqliteBackupOpen(path, mode string) (*sql.DB, error) {
	if !filepath.IsAbs(path) {
		var err error
		path, err = filepath.Abs(path)
		if err != nil {
			return nil, err
		}
	}
	db, e := sql.Open("sqlite", sqliteBackupURI(path, mode))
	if e == nil {
		db.SetMaxOpenConns(1)
		db.SetMaxIdleConns(0)
	}
	return db, e
}
func sqliteBackupIntegrity(ctx context.Context, path string) error {
	if st, err := os.Stat(path); err != nil || !st.Mode().IsRegular() {
		return errors.Join(err, errors.New("backup must be a regular SQLite file"))
	}
	// An exported standard backup is self-contained. A live WAL file does not
	// have a stable digest and cannot be used as an immutable restore archive.
	if st, e := os.Stat(path + "-wal"); e == nil && st.Size() > 0 {
		return errors.New("checkpoint or export the source database before restoring")
	}
	db, e := sqliteBackupOpen(path, "ro")
	if e != nil {
		return e
	}
	defer db.Close()
	var result string
	e = db.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&result)
	if e != nil || result != "ok" {
		return errors.Join(e, errors.New("SQLite integrity check failed"))
	}
	return nil
}
func (t *backupTask) executeSQLiteBackup(progress func(contract.BackupProgress)) (out contract.BackupOutcome) {
	out.State = "not_executed"
	r := t.plan.Request
	if r.Action == "restore" && r.Overwrite {
		if !sameBackupFile(t.plan.Source, r.Destination) {
			out.Error = contract.ErrRevoked
			return
		}
		db, e := sqliteBackupOpen(t.plan.Source, "rw")
		if e != nil {
			out.Error = e
			return
		}
		defer db.Close()
		c, e := db.Conn(t.ctx)
		if e != nil {
			out.Error = e
			return
		}
		defer c.Close()
		complete, e := sqliteOnlineCopy(t.ctx, c, r.Path, true, progress)
		out.Error = e
		out.Path = t.plan.Source
		if complete && e == nil {
			out.State = "completed"
		} else if complete {
			out.State = "uncertain"
		} else {
			out.State = "failed"
		}
		return
	}
	destination, source := r.Path, t.plan.Source
	if r.Action == "restore" {
		destination, source = r.Destination, r.Path
	}
	if _, e := os.Lstat(destination); !os.IsNotExist(e) {
		out.Error = errors.New("backup destination already exists or cannot be checked")
		return
	}
	dir, e := os.MkdirTemp(filepath.Dir(destination), ".nyadb-backup-*")
	if e != nil {
		out.Error = e
		return
	}
	defer os.RemoveAll(dir)
	tmp, e := os.CreateTemp(dir, "database-*.sqlite")
	if e != nil {
		out.Error = e
		return
	}
	path := tmp.Name()
	tmp.Close()
	db, e := sqliteBackupOpen(source, "ro")
	if e != nil {
		out.Error = e
		return
	}
	defer db.Close()
	c, e := db.Conn(t.ctx)
	if e != nil {
		out.Error = e
		return
	}
	defer c.Close()
	_, e = sqliteOnlineCopy(t.ctx, c, path, false, progress)
	if e != nil {
		out.Error = e
		out.State = "failed"
		return
	}
	if e = sqliteBackupIntegrity(t.ctx, path); e != nil {
		out.Error = e
		out.State = "failed"
		return
	}
	if e = t.ctx.Err(); e != nil {
		out.Error = e
		return
	}
	// A no-clobber publication keeps a concurrently created destination intact.
	if e = os.Link(path, destination); e != nil {
		out.Error = e
		return
	}
	out.Path, out.State = destination, "completed"
	return
}
func sqliteOnlineCopy(ctx context.Context, c *sql.Conn, remote string, restore bool, progress func(contract.BackupProgress)) (complete bool, err error) {
	err = c.Raw(func(raw any) error {
		api, ok := raw.(interface {
			NewBackup(string) (*sqlitedriver.Backup, error)
			NewRestore(string) (*sqlitedriver.Backup, error)
		})
		if !ok {
			return contract.ErrUnsupported
		}
		var b *sqlitedriver.Backup
		var e error
		if restore {
			b, e = api.NewRestore(sqliteBackupURI(remote, "ro"))
		} else {
			b, e = api.NewBackup(sqliteBackupURI(remote, "rwc"))
		}
		if e != nil {
			return e
		}
		for {
			if e = ctx.Err(); e != nil {
				break
			}
			var more bool
			more, e = b.Step(128)
			if e != nil {
				break
			}
			progress(contract.BackupProgress{Phase: "copy", Completed: int64(b.PageCount() - b.Remaining()), Total: int64(b.PageCount())})
			if !more {
				complete = true
				break
			}
		}
		finish := b.Finish()
		if e != nil || finish != nil {
			return fmt.Errorf("SQLite online copy failed: %w", errors.Join(e, finish))
		}
		return nil
	})
	return
}
