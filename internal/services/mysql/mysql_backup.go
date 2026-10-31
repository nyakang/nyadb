package mysql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/mysqlddl"
	"github.com/nyakang/nyadb/internal/db/sqlresult"
	"github.com/nyakang/nyadb/internal/services/backups"
)

type BackupRequest struct {
	Database   string
	Generation uint64
	Data       bool
	Tables     []string
}
type backupDefinition struct {
	object      backups.Object
	ddl         string
	fingerprint string
}

func backupCatalog(ctx context.Context, c *sql.Conn, db string, tables []string) ([]backupDefinition, error) {
	var all []backupDefinition
	database, e := readObjectDefinition(ctx, c, ObjectIdentity{Database: db, Kind: "database"})
	if e != nil {
		return nil, e
	}
	all = append(all, backupDefinition{object: backups.Object{Kind: "DATABASE", Name: db, SQLMode: database.SQLMode}, ddl: database.SQL, fingerprint: database.Fingerprint})
	rows, e := c.QueryContext(ctx, "SELECT TABLE_NAME,TABLE_TYPE,COALESCE(ENGINE,'') FROM information_schema.TABLES WHERE TABLE_SCHEMA=? ORDER BY TABLE_NAME", db)
	if e != nil {
		return nil, e
	}
	type table struct{ name, kind, engine string }
	var names []table
	for rows.Next() {
		var t table
		if e = rows.Scan(&t.name, &t.kind, &t.engine); e != nil {
			rows.Close()
			return nil, e
		}
		if len(names) >= backups.MaxObjects {
			rows.Close()
			return nil, ErrMySQLMetadataLimit
		}
		names = append(names, t)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}
	selected := map[string]bool{}
	for _, t := range tables {
		selected[t] = true
	}
	found := 0
	for _, t := range names {
		if len(selected) > 0 && !selected[t.name] {
			continue
		}
		found++
		if t.kind == "VIEW" {
			v, e := readObjectDefinition(ctx, c, ObjectIdentity{Database: db, Name: t.name, Kind: "view"})
			if e != nil {
				return nil, e
			}
			all = append(all, backupDefinition{backups.Object{Kind: "VIEW", Name: t.name, SQLMode: v.SQLMode}, v.SQL, v.Fingerprint})
			continue
		}
		snap, e := readTableDesign(ctx, c, db, t.name)
		if e != nil {
			return nil, e
		}
		if snap.TriggersUnknown {
			return nil, errors.New("complete backup requires confirmed trigger visibility for " + t.name)
		}
		cols, e := readMySQLColumnMetadata(ctx, c, db, t.name, 128)
		if e != nil {
			return nil, e
		}
		o := backups.Object{Kind: "TABLE", Name: t.name, Engine: t.engine, SQLMode: snap.SQLMode, Consistency: "consistent-read"}
		if !strings.EqualFold(t.engine, "InnoDB") {
			o.Consistency = "not-guaranteed"
		}
		for _, col := range cols {
			if !MySQLDataColumnGenerated(col) {
				o.Columns = append(o.Columns, contract.Column{Name: col.Name, DatabaseType: col.DataType, Nullable: col.Nullable})
			}
		}
		all = append(all, backupDefinition{o, snap.DDL, snap.Fingerprint()})
		for _, tr := range snap.Triggers {
			all = append(all, backupDefinition{backups.Object{Kind: "TRIGGER", Name: tr.Name, Table: t.name, SQLMode: tr.SQLMode, Dependencies: []string{"TABLE:" + t.name}}, tr.SQL, mysqlddl.Canonical(tr.SQL) + "\x00" + tr.SQLMode})
		}
	}
	if len(selected) > 0 && found != len(selected) {
		return nil, errors.New("a selected backup object disappeared")
	}
	if len(selected) > 0 {
		return all, nil
	}
	for _, kind := range []string{"FUNCTION", "PROCEDURE", "EVENT"} {
		var query string
		if kind == "EVENT" {
			query = "SELECT EVENT_NAME FROM information_schema.EVENTS WHERE EVENT_SCHEMA=? ORDER BY EVENT_NAME"
		} else {
			query = "SELECT ROUTINE_NAME FROM information_schema.ROUTINES WHERE ROUTINE_SCHEMA=? AND ROUTINE_TYPE='" + kind + "' ORDER BY ROUTINE_NAME"
		}
		rows, e = c.QueryContext(ctx, query, db)
		if e != nil {
			return nil, e
		}
		var ns []string
		for rows.Next() {
			var n string
			if e = rows.Scan(&n); e != nil {
				rows.Close()
				return nil, e
			}
			if len(ns) >= backups.MaxObjects {
				rows.Close()
				return nil, ErrMySQLMetadataLimit
			}
			ns = append(ns, n)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return nil, e
		}
		for _, n := range ns {
			v, e := readObjectDefinition(ctx, c, ObjectIdentity{Database: db, Name: n, Kind: strings.ToLower(kind)})
			if e != nil {
				return nil, e
			}
			all = append(all, backupDefinition{backups.Object{Kind: kind, Name: n, SQLMode: v.SQLMode}, v.SQL, v.Fingerprint})
		}
	}
	if len(all) > backups.MaxObjects {
		return nil, ErrMySQLMetadataLimit
	}
	return all, nil
}

// BackupMySQL uses one original session and one repeatable-read snapshot.
// Non-InnoDB rows are explicitly marked; a failed or changed structure never
// returns a completed archive to the caller's atomic publishing writer.
func (m *MySQLSessionManager) BackupMySQL(ctx context.Context, id int64, request BackupRequest, w io.Writer) (backups.Manifest, error) {
	if _, e := quotedMySQLIdentifier(request.Database); e != nil {
		return backups.Manifest{}, e
	}
	s, qctx, cancel, e := m.requestContext(ctx, id, 0)
	if e != nil {
		return backups.Manifest{}, e
	}
	defer cancel()
	valid := func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.sessions[id] == s && m.versions[id] == request.Generation && s.ctx.Err() == nil
	}
	if !valid() {
		return backups.Manifest{}, ErrMySQLNotConnected
	}
	c, e := s.db.Conn(qctx)
	if e != nil {
		return backups.Manifest{}, e
	}
	defer c.Close()
	defer c.Raw(func(any) error { return driver.ErrBadConn })
	if _, e = c.ExecContext(qctx, "SET NAMES utf8mb4"); e != nil {
		return backups.Manifest{}, e
	}
	if _, e = c.ExecContext(qctx, "SET SESSION time_zone='+00:00'"); e != nil {
		return backups.Manifest{}, e
	}
	var version string
	if e = c.QueryRowContext(qctx, "SELECT VERSION()").Scan(&version); e != nil {
		return backups.Manifest{}, e
	}
	if _, e = c.ExecContext(qctx, "SET TRANSACTION ISOLATION LEVEL REPEATABLE READ"); e != nil {
		return backups.Manifest{}, e
	}
	if _, e = c.ExecContext(qctx, "START TRANSACTION WITH CONSISTENT SNAPSHOT, READ ONLY"); e != nil {
		return backups.Manifest{}, e
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 2*time.Second)
		defer stop()
		_, _ = c.ExecContext(cleanup, "ROLLBACK")
	}()
	before, e := backupCatalog(qctx, c, request.Database, request.Tables)
	if e != nil {
		return backups.Manifest{}, e
	}
	archive := backups.NewWriter(w, request.Database, version)
	archive.Warning("Object inventory is limited to objects visible to the authenticated account; no server-wide completeness is claimed.")
	for _, d := range before {
		var stream func(func([]contract.Cell) error) error
		if request.Data && d.object.Kind == "TABLE" {
			if d.object.Consistency == "not-guaranteed" {
				archive.Warning("Consistent rows cannot be guaranteed for " + d.object.Name + " (" + d.object.Engine + ")")
			}
			o := d.object
			stream = func(emit func([]contract.Cell) error) error {
				var columns []string
				for _, col := range o.Columns {
					q, _ := quotedMySQLIdentifier(col.Name)
					columns = append(columns, q)
				}
				if len(columns) == 0 {
					return errors.New("table has no writable backup columns")
				}
				dbq, _ := quotedMySQLIdentifier(request.Database)
				tq, _ := quotedMySQLIdentifier(o.Name)
				rows, e := c.QueryContext(qctx, "SELECT "+strings.Join(columns, ",")+" FROM "+dbq+"."+tq)
				if e != nil {
					return e
				}
				defer rows.Close()
				_, e = sqlresult.Scan(qctx, rows, sqlresult.Limits{Rows: 0, Bytes: 1 << 20, CellBytes: 64 << 10, Columns: 128}, func(_ int, _ []contract.Column, row []contract.Cell) error {
					if row == nil {
						return nil
					}
					return emit(row)
				})
				return e
			}
		}
		if e = archive.Add(qctx, d.object, d.ddl, stream); e != nil {
			return backups.Manifest{}, e
		}
	}
	after, e := backupCatalog(qctx, c, request.Database, request.Tables)
	if e != nil {
		return backups.Manifest{}, e
	}
	if !reflect.DeepEqual(before, after) {
		return backups.Manifest{}, errors.New("backup structure changed during the read; complete archive not published")
	}
	if !valid() {
		return backups.Manifest{}, ErrMySQLNotConnected
	}
	return archive.Finish(qctx)
}

// SQLDump emits complete public DDL and exact typed INSERT literals. It shares
// the verified backup snapshot rather than reading tables a second time.
func SQLDump(ctx context.Context, r *backups.Reader, w io.Writer, data bool) error {
	m := r.Manifest()
	dbq, e := quotedMySQLIdentifier(m.Database)
	if e != nil {
		return e
	}
	if _, e = io.WriteString(w, "-- NyaDB SQL dump v1; visible objects only\nSET NAMES utf8mb4;\nSET SESSION time_zone='+00:00';\n"); e != nil {
		return e
	}
	var tables, programs, late []RestoreStep
	var constraints []string
	for i, o := range m.Objects {
		ddl, e := r.DDL(i)
		if e != nil {
			return e
		}
		if o.Kind == "DATABASE" {
			ddl = "CREATE DATABASE IF NOT EXISTS" + ddl[len("CREATE DATABASE"):]
			if e = dumpRawDefinition(w, ddl, o.SQLMode); e != nil {
				return e
			}
			continue
		}
		step := RestoreStep{Kind: o.Kind, Name: o.Name, SQL: ddl, Index: i}
		switch o.Kind {
		case "TABLE":
			table, e := mysqlddl.Parse(m.Database, o.Name, ddl)
			if e != nil {
				return e
			}
			var kept []mysqlddl.Clause
			for _, c := range table.Clauses {
				if c.Kind == mysqlddl.Foreign {
					tq, _ := quotedMySQLIdentifier(o.Name)
					constraints = append(constraints, "ALTER TABLE "+dbq+"."+tq+" ADD "+c.SQL)
				} else {
					kept = append(kept, c)
				}
			}
			table.Clauses = kept
			step.SQL = table.DDL()
			tables = append(tables, step)
		case "TRIGGER", "EVENT":
			late = append(late, step)
		default:
			programs = append(programs, step)
		}
	}
	if _, e = fmt.Fprintf(w, "USE %s;\n", dbq); e != nil {
		return e
	}
	sort.SliceStable(programs, func(i, j int) bool { return programs[i].Kind != "VIEW" && programs[j].Kind == "VIEW" })
	programs, e = orderRestoreViews(programs)
	if e != nil {
		return e
	}
	for _, step := range append(tables, programs...) {
		if e = dumpRawDefinition(w, step.SQL, m.Objects[step.Index].SQLMode); e != nil {
			return e
		}
	}
	if data {
		for _, step := range tables {
			if m.Objects[step.Index].Data != "" {
				if e = dumpRows(ctx, r, w, step.Index); e != nil {
					return e
				}
			}
		}
	}
	for _, statement := range constraints {
		if e = dumpRawDefinition(w, statement, ""); e != nil {
			return e
		}
	}
	for _, step := range late {
		if e = dumpDefinition(ctx, r, w, step.Index); e != nil {
			return e
		}
	}
	return ctx.Err()
}

func dumpDefinition(ctx context.Context, r *backups.Reader, w io.Writer, i int) error {
	if e := ctx.Err(); e != nil {
		return e
	}
	o := r.Manifest().Objects[i]
	ddl, e := r.DDL(i)
	if e != nil {
		return e
	}
	return dumpRawDefinition(w, ddl, o.SQLMode)
}
func dumpRawDefinition(w io.Writer, ddl, mode string) error {
	delimiter := "$$NYADB$$"
	for n := 0; strings.Contains(ddl, delimiter); n++ {
		delimiter = fmt.Sprintf("$N%010d$", n)
	}
	_, e := fmt.Fprintf(w, "SET SESSION sql_mode = %s;\nDELIMITER %s\n%s%s\nDELIMITER ;\n", dumpText(mode), delimiter, ddl, delimiter)
	return e
}

func dumpRows(ctx context.Context, r *backups.Reader, w io.Writer, i int) error {
	m := r.Manifest()
	o := m.Objects[i]
	rows, e := r.Rows(i)
	if e != nil {
		return e
	}
	defer rows.Close()
	dbq, _ := quotedMySQLIdentifier(m.Database)
	tq, _ := quotedMySQLIdentifier(o.Name)
	var cols []string
	for _, col := range o.Columns {
		q, _ := quotedMySQLIdentifier(col.Name)
		cols = append(cols, q)
	}
	for {
		cells, e := rows.Next(ctx)
		if e == io.EOF {
			return nil
		}
		if e != nil {
			return e
		}
		values := make([]string, len(cells))
		var variables []string
		for j, c := range cells {
			variable := fmt.Sprintf("@nyadb_dump_cell_%d", j)
			values[j], e = dumpStreamCell(ctx, w, o.Columns[j], c, variable)
			if e != nil {
				return e
			}
			if len(c.Text)+len(c.Bytes) > 16<<10 {
				variables = append(variables, variable)
			}
		}
		if _, e = fmt.Fprintf(w, "INSERT INTO %s.%s (%s) VALUES (%s);\n", dbq, tq, strings.Join(cols, ","), strings.Join(values, ",")); e != nil {
			return e
		}
		if len(variables) > 0 {
			var clears []string
			for _, v := range variables {
				clears = append(clears, v+"=NULL")
			}
			if _, e = fmt.Fprintf(w, "SET %s;\n", strings.Join(clears, ",")); e != nil {
				return e
			}
		}
	}
}

// DumpMySQL spools the verified snapshot into a private temporary backup before
// converting it to SQL. The caller publishes the destination atomically.
func (m *MySQLSessionManager) DumpMySQL(ctx context.Context, id int64, request BackupRequest, w io.Writer) error {
	f, e := os.CreateTemp("", "nyadb-dump-*.zip")
	if e != nil {
		return e
	}
	path := f.Name()
	defer os.Remove(path)
	_, e = m.BackupMySQL(ctx, id, request, f)
	ce := f.Close()
	if e != nil {
		return e
	}
	if ce != nil {
		return ce
	}
	r, e := backups.Open(ctx, path)
	if e != nil {
		return e
	}
	defer r.Close()
	return SQLDump(ctx, r, w, request.Data)
}
