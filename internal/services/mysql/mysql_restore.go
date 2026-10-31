package mysql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/mysqlddl"
	"github.com/nyakang/nyadb/internal/db/sqltext"
	"github.com/nyakang/nyadb/internal/services/backups"
)

type RestoreStep struct {
	SQL, Kind, Name string
	Index           int
	Rows            int64
}
type RestorePlan struct {
	Database, Policy string
	ProfileID        int64
	Generation       uint64
	Steps            []RestoreStep
	Fingerprints     map[string]string
	reader           *backups.Reader
}

// RebindBackupDDL changes schema identifiers only, retaining literals, complete
// routine bodies and definer clauses. Ambiguous executable comments fail closed.
func RebindBackupDDL(raw, source, target string) (string, error) {
	if _, e := quotedMySQLIdentifier(target); e != nil {
		return "", e
	}
	if source == target {
		return raw, nil
	}
	ts, e := sqltext.Tokens(raw)
	if e != nil {
		return "", e
	}
	var rs []sqlReplacement
	q, _ := quotedMySQLIdentifier(target)
	for i, t := range ts {
		if t.Comment && strings.HasPrefix(t.Text, "/*!") && strings.Contains(t.Text, source) {
			return "", errors.New("cannot safely remap a schema inside an executable comment")
		}
		if i+1 < len(ts) && ts[i+1].Text == "." && unquoteObjectToken(t) == source {
			rs = append(rs, sqlReplacement{t.Start, t.End, q})
		}
		if i > 0 && (sqltext.Word(ts[i-1], "DATABASE") || sqltext.Word(ts[i-1], "SCHEMA")) && unquoteObjectToken(t) == source {
			rs = append(rs, sqlReplacement{t.Start, t.End, q})
		}
	}
	return replaceSQL(raw, rs), nil
}
func restoreFingerprint(ctx context.Context, c *sql.Conn, db, kind, name string) (string, error) {
	if kind == "TRIGGER" {
		var table string
		e := c.QueryRowContext(ctx, "SELECT EVENT_OBJECT_TABLE FROM information_schema.TRIGGERS WHERE TRIGGER_SCHEMA=? AND TRIGGER_NAME=?", db, name).Scan(&table)
		if errors.Is(e, sql.ErrNoRows) {
			return "", nil
		}
		if e != nil {
			return "", e
		}
		var mode string
		ddl, e := readCreateMode(ctx, c, "TRIGGER", db, name, &mode)
		if e != nil {
			return "", e
		}
		return "TRIGGER:" + definitionFingerprint(ObjectDefinition{SQL: ddl, SQLMode: mode}) + ":" + table, nil
	}
	var n int
	if kind == "TABLE" || kind == "VIEW" {
		var actual string
		e := c.QueryRowContext(ctx, "SELECT TABLE_TYPE FROM information_schema.TABLES WHERE TABLE_SCHEMA=? AND TABLE_NAME=?", db, name).Scan(&actual)
		if errors.Is(e, sql.ErrNoRows) {
			return "", nil
		}
		if e != nil {
			return "", e
		}
		if actual == "VIEW" {
			kind = "VIEW"
		} else {
			kind = "TABLE"
		}
	} else {
		exists, e := objectExists(ctx, c, ObjectIdentity{Database: db, Name: name, Kind: strings.ToLower(kind)})
		if e != nil {
			return "", e
		}
		if !exists {
			return "", nil
		}
		n = 1
	}
	_ = n
	if kind == "TABLE" {
		v, e := readTableDesign(ctx, c, db, name)
		if e != nil {
			return "", e
		}
		return "TABLE:" + v.Fingerprint(), nil
	}
	v, e := readObjectDefinition(ctx, c, ObjectIdentity{Database: db, Name: name, Kind: strings.ToLower(kind)})
	return kind + ":" + v.Fingerprint, e
}

// PlanRestore performs checksum/row validation before this call and reads all
// existing targets. Default stop aborts before any DDL or data mutation.
func (m *MySQLSessionManager) PlanRestore(ctx context.Context, id int64, generation uint64, database, policy string, r *backups.Reader) (RestorePlan, error) {
	p := RestorePlan{ProfileID: id, Generation: generation, Database: database, Policy: policy, reader: r, Fingerprints: map[string]string{}}
	if policy != "stop" && policy != "replace" || r == nil {
		return p, errors.New("invalid restore policy")
	}
	if _, e := quotedMySQLIdentifier(database); e != nil {
		return p, e
	}
	s, qctx, cancel, e := m.requestContext(ctx, id, 0)
	if e != nil {
		return p, e
	}
	defer cancel()
	if m.SessionGeneration(id) != generation {
		return p, ErrMySQLNotConnected
	}
	c, e := s.db.Conn(qctx)
	if e != nil {
		return p, e
	}
	defer c.Close()
	var count int
	if e = c.QueryRowContext(qctx, "SELECT COUNT(*) FROM information_schema.SCHEMATA WHERE SCHEMA_NAME=?", database).Scan(&count); e != nil {
		return p, e
	}
	manifest := r.Manifest()
	var drops, creates, data, constraints, late []RestoreStep
	tableNames := map[string]bool{}
	for _, o := range manifest.Objects {
		if o.Kind == "TABLE" {
			tableNames[o.Name] = true
		}
	}
	if policy == "replace" {
		rows, e := c.QueryContext(qctx, "SELECT TABLE_SCHEMA,TABLE_NAME,REFERENCED_TABLE_NAME FROM information_schema.KEY_COLUMN_USAGE WHERE REFERENCED_TABLE_SCHEMA=?", database)
		if e != nil {
			return p, e
		}
		for rows.Next() {
			var schema, name, referenced string
			if e = rows.Scan(&schema, &name, &referenced); e != nil {
				rows.Close()
				return p, e
			}
			if tableNames[referenced] && (schema != database || !tableNames[name]) {
				rows.Close()
				return p, fmt.Errorf("restore blocked by an external incoming foreign key: %s.%s references %s", schema, name, referenced)
			}
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return p, e
		}
	}
	for i, o := range manifest.Objects {
		ddl, e := r.DDL(i)
		if e != nil {
			return p, e
		}
		ddl, e = RebindBackupDDL(ddl, manifest.Database, database)
		if e != nil {
			return p, e
		}
		if o.Kind == "DATABASE" {
			if o.Name != manifest.Database {
				return p, errors.New("backup database identity differs from manifest")
			}
			if e = ValidateObjectSQL(ObjectIdentity{Database: database, Kind: "database"}, "create", ddl); e != nil {
				return p, e
			}
			if count == 0 {
				creates = append(creates, RestoreStep{SQL: ddl, Kind: o.Kind, Name: database, Index: i})
			}
			continue
		}
		if o.Kind == "TRIGGER" {
			if !tableNames[o.Table] {
				return p, errors.New("backup trigger target table is absent")
			}
			if e = mysqlddl.ValidateTrigger(mysqlddl.Trigger{Name: o.Name, SQL: ddl, SQLMode: o.SQLMode}, database, o.Table); e != nil {
				return p, e
			}
			fp, e := restoreFingerprint(qctx, c, database, o.Kind, o.Name)
			if e != nil {
				return p, e
			}
			p.Fingerprints[o.Kind+"\x00"+o.Name] = fp
			if fp != "" && (policy == "stop" || !strings.HasSuffix(fp, ":"+o.Table)) {
				return p, fmt.Errorf("restore trigger conflict: %s", o.Name)
			}
			late = append(late, RestoreStep{SQL: ddl, Kind: o.Kind, Name: o.Name, Index: i})
			continue
		}
		if o.Kind != "TABLE" {
			if e = ValidateObjectSQL(ObjectIdentity{Database: database, Name: o.Name, Kind: strings.ToLower(o.Kind)}, "create", ddl); e != nil {
				return p, e
			}
		}
		switch o.Kind {
		case "TABLE", "VIEW", "FUNCTION", "PROCEDURE", "EVENT":
		default:
			return p, errors.New("unsupported backup object kind")
		}
		fp, e := restoreFingerprint(qctx, c, database, o.Kind, o.Name)
		if e != nil {
			return p, e
		}
		key := o.Kind + "\x00" + o.Name
		p.Fingerprints[key] = fp
		if fp != "" {
			if policy == "stop" {
				return p, fmt.Errorf("restore target already exists: %s %s", o.Kind, o.Name)
			}
			actualKind := strings.SplitN(fp, ":", 2)[0]
			dbq, _ := quotedMySQLIdentifier(database)
			nq, _ := quotedMySQLIdentifier(o.Name)
			drops = append(drops, RestoreStep{SQL: "DROP " + actualKind + " " + dbq + "." + nq, Kind: actualKind, Name: o.Name, Index: i})
		}
		if o.Kind == "TABLE" {
			t, e := mysqlddl.Parse(database, o.Name, ddl)
			if e != nil {
				return p, e
			}
			var kept []mysqlddl.Clause
			for _, clause := range t.Clauses {
				if clause.Kind == mysqlddl.Foreign {
					dbq, _ := quotedMySQLIdentifier(database)
					tq, _ := quotedMySQLIdentifier(o.Name)
					constraints = append(constraints, RestoreStep{SQL: "ALTER TABLE " + dbq + "." + tq + " ADD " + clause.SQL, Kind: "FOREIGN KEY", Name: o.Name, Index: i})
				} else {
					kept = append(kept, clause)
				}
			}
			t.Clauses = kept
			ddl = t.DDL()
		}
		step := RestoreStep{SQL: ddl, Kind: o.Kind, Name: o.Name, Index: i}
		if o.Kind == "EVENT" {
			late = append(late, step)
		} else {
			creates = append(creates, step)
		}
		if o.Data != "" && o.Rows > 0 {
			data = append(data, RestoreStep{Kind: "DATA", Name: o.Name, Rows: o.Rows, Index: i})
		}
	}
	priority := func(kind string) int {
		switch kind {
		case "DATABASE":
			return 0
		case "TABLE":
			return 1
		case "FUNCTION":
			return 2
		case "PROCEDURE":
			return 3
		case "VIEW":
			return 4
		case "EVENT":
			return 5
		}
		return 6
	}
	sort.SliceStable(creates, func(i, j int) bool { return priority(creates[i].Kind) < priority(creates[j].Kind) })
	// Views may depend on other views. Topologically order their public FROM/JOIN
	// references; unresolved cycles stop before any restore step is run.
	creates, e = orderRestoreViews(creates)
	if e != nil {
		return p, e
	}
	sort.SliceStable(drops, func(i, j int) bool { return priority(drops[i].Kind) > priority(drops[j].Kind) })
	p.Steps = append(p.Steps, drops...)
	p.Steps = append(p.Steps, creates...)
	p.Steps = append(p.Steps, data...)
	p.Steps = append(p.Steps, constraints...)
	p.Steps = append(p.Steps, late...)
	return p, nil
}
func orderRestoreViews(steps []RestoreStep) ([]RestoreStep, error) {
	byname := map[string]RestoreStep{}
	for _, s := range steps {
		if s.Kind == "VIEW" {
			byname[s.Name] = s
		}
	}
	var out []RestoreStep
	done := map[string]bool{}
	visiting := map[string]bool{}
	var visit func(string) error
	visit = func(name string) error {
		if done[name] {
			return nil
		}
		if visiting[name] {
			return errors.New("view dependency cycle in backup")
		}
		visiting[name] = true
		s := byname[name]
		ts, e := sqltext.Tokens(s.SQL)
		if e != nil {
			return e
		}
		for i, t := range ts {
			if (sqltext.Word(t, "FROM") || sqltext.Word(t, "JOIN")) && i+1 < len(ts) {
				j := i + 1
				if j+2 < len(ts) && ts[j+1].Text == "." {
					j += 2
				}
				dep := unquoteObjectToken(ts[j])
				if _, exists := byname[dep]; exists {
					if e = visit(dep); e != nil {
						return e
					}
				}
			}
		}
		visiting[name] = false
		done[name] = true
		out = append(out, s)
		return nil
	}
	for _, s := range steps {
		if s.Kind != "VIEW" {
			out = append(out, s)
		} else if e := visit(s.Name); e != nil {
			return nil, e
		}
	}
	return out, nil
}

type backupRowSource struct {
	rows    *backups.Rows
	columns []contract.Column
}

func (s *backupRowSource) Close() error { return s.rows.Close() }
func (s *backupRowSource) Next(ctx context.Context) (MySQLDataEdit, error) {
	cells, e := s.rows.Next(ctx)
	if e != nil {
		return MySQLDataEdit{}, e
	}
	edit := MySQLDataEdit{Kind: "insert", Values: map[string]MySQLDataValue{}}
	for i, c := range cells {
		v := MySQLDataValue{Text: c.Text}
		if c.Kind == contract.Null {
			v.Null = true
		}
		if c.Kind == contract.Binary {
			v.Binary = true
			v.Bytes = append([]byte{}, c.Bytes...)
		}
		edit.Values[s.columns[i].Name] = v
	}
	return edit, nil
}

// RestoreMySQL executes the reviewed plan once. Each table's rows reuse the
// existing streaming importer and transaction receipts; DDL is never described
// as rolled back after a later failure.
func (m *MySQLSessionManager) RestoreMySQL(ctx context.Context, plan RestorePlan) DDLOutcome {
	out := DDLOutcome{State: "not_executed"}
	if plan.reader == nil {
		out.Error = errors.New("backup reader is closed")
		return out
	}
	// Rebuild exact reviewed statements and recheck every original target.
	fresh, e := m.PlanRestore(ctx, plan.ProfileID, plan.Generation, plan.Database, plan.Policy, plan.reader)
	if e != nil {
		out.Error = e
		return out
	}
	if fmt.Sprint(fresh.Steps) != fmt.Sprint(plan.Steps) || fmt.Sprint(fresh.Fingerprints) != fmt.Sprint(plan.Fingerprints) {
		out.Error = errors.New("restore target changed after preview")
		return out
	}
	s, qctx, cancel, e := m.requestContext(ctx, plan.ProfileID, 0)
	if e != nil {
		out.Error = e
		return out
	}
	defer cancel()
	valid := func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.sessions[plan.ProfileID] == s && m.versions[plan.ProfileID] == plan.Generation && s.ctx.Err() == nil
	}
	if !valid() {
		out.Error = ErrMySQLNotConnected
		return out
	}
	c, e := s.db.Conn(qctx)
	if e != nil {
		out.Error = e
		return out
	}
	defer c.Close()
	defer c.Raw(func(any) error { return driver.ErrBadConn })
	if _, e = c.ExecContext(qctx, "SET NAMES utf8mb4"); e != nil {
		out.Error = e
		return out
	}
	if _, e = c.ExecContext(qctx, "SET SESSION time_zone='+00:00'"); e != nil {
		out.Error = e
		return out
	}
	// Recheck immediately on the executing physical connection. This detects
	// prior external changes, and cannot eliminate later external DDL races.
	for key, want := range plan.Fingerprints {
		parts := strings.SplitN(key, "\x00", 2)
		got, e := restoreFingerprint(qctx, c, plan.Database, parts[0], parts[1])
		if e != nil || got != want {
			out.Error = errors.Join(errors.New("external DDL conflict before restore"), e)
			return out
		}
	}
	manifest := plan.reader.Manifest()
	for _, step := range plan.Steps {
		sql := step.SQL
		if step.Kind == "DATA" {
			sql = fmt.Sprintf("IMPORT %d typed rows INTO %s.%s", step.Rows, plan.Database, step.Name)
		}
		out.Steps = append(out.Steps, DDLStep{SQL: sql, State: "not_executed"})
	}
	for i, step := range plan.Steps {
		if !valid() || qctx.Err() != nil {
			out.Error = errors.Join(ErrMySQLNotConnected, qctx.Err())
			if i > 0 {
				out.State = "partial"
			}
			return out
		}
		if step.Kind == "DATA" {
			o := manifest.Objects[step.Index]
			if !strings.EqualFold(o.Engine, "InnoDB") {
				rows, err := plan.reader.Rows(step.Index)
				if err != nil {
					out.Error = err
					if i > 0 {
						out.State = "partial"
					}
					return out
				}
				result := restoreNontransactionalData(qctx, c, plan.Database, step.Name, &backupRowSource{rows: rows, columns: o.Columns}, step.Rows, valid)
				e = result.Error
				out.Steps[i].SQL += fmt.Sprintf(" · %d/%d rows acknowledged (nontransactional)", result.RowsApplied, step.Rows)
				if e != nil {
					out.Steps[i].State = result.State
					if result.State == "uncertain" {
						out.State = "uncertain"
					}
				}
			} else {
				result, err := m.ImportMySQLDataStream(qctx, plan.ProfileID, plan.Database, step.Name, MySQLDataImportStreamRequest{Generation: plan.Generation, CanonicalBackupValues: true, Rows: step.Rows, Request: MySQLDataImportRequest{Mode: "append"}, Open: func(context.Context) (MySQLDataImportRowSource, error) {
					rows, e := plan.reader.Rows(step.Index)
					if e != nil {
						return nil, e
					}
					return &backupRowSource{rows: rows, columns: o.Columns}, nil
				}})
				e = err
				if e != nil && result != nil && result.CommitUncertain {
					out.Steps[i].State = "uncertain"
					out.State = "uncertain"
				}
			}
		} else {
			if step.Kind != "DATABASE" {
				dbq, _ := quotedMySQLIdentifier(plan.Database)
				_, e = c.ExecContext(qctx, "USE "+dbq)
				if e != nil {
					out.Error = e
					if i > 0 {
						out.State = "partial"
					}
					return out
				}
			}
			mode := manifest.Objects[step.Index].SQLMode
			if _, e = c.ExecContext(qctx, "SET SESSION sql_mode=?", mode); e != nil {
				out.Error = e
				if i > 0 {
					out.State = "partial"
				}
				return out
			}
			checks := 1
			if strings.HasPrefix(step.SQL, "DROP ") {
				checks = 0
			}
			if _, e = c.ExecContext(qctx, "SET SESSION foreign_key_checks=?", checks); e != nil {
				out.Error = e
				if i > 0 {
					out.State = "partial"
				}
				return out
			}
			_, e = c.ExecContext(qctx, step.SQL)
		}
		if e != nil {
			out.Error = e
			out.Steps[i].Error = e
			var server *mysqldriver.MySQLError
			if out.State != "uncertain" {
				if errors.As(e, &server) || step.Kind == "DATA" && !errors.Is(e, ErrMySQLDataCommitUncertain) {
					if i > 0 {
						out.State = "partial"
					}
				} else {
					out.State = "uncertain"
					out.Steps[i].State = "uncertain"
				}
			}
			return out
		}
		out.Steps[i].State = "executed"
		out.State = "executed"
	}
	if !valid() {
		out.Error = ErrMySQLNotConnected
		out.State = "uncertain"
		return out
	}
	// Complete definitions must be readable after execution. The table designer
	// compares all supported and unknown preserved clauses through canonical DDL.
	for i, o := range manifest.Objects {
		if o.Kind == "DATABASE" || o.Kind == "TRIGGER" {
			continue
		}
		if o.Kind == "TABLE" {
			actual, e := readTableDesign(qctx, c, plan.Database, o.Name)
			if e != nil {
				out.Error = e
				out.State = "uncertain"
				return out
			}
			ddl, e := plan.reader.DDL(i)
			if e != nil {
				out.Error = e
				return out
			}
			ddl, e = RebindBackupDDL(ddl, manifest.Database, plan.Database)
			if e != nil {
				out.Error = e
				return out
			}
			table, e := mysqlddl.Parse(plan.Database, o.Name, ddl)
			if e != nil {
				out.Error = e
				return out
			}
			var triggers []mysqlddl.Trigger
			for j, tr := range manifest.Objects {
				if tr.Kind == "TRIGGER" && tr.Table == o.Name {
					raw, e := plan.reader.DDL(j)
					if e != nil {
						out.Error = e
						return out
					}
					raw, e = RebindBackupDDL(raw, manifest.Database, plan.Database)
					if e != nil {
						out.Error = e
						return out
					}
					triggers = append(triggers, mysqlddl.Trigger{Name: tr.Name, SQL: raw, SQLMode: tr.SQLMode})
				}
			}
			if e = mysqlddl.Verify(mysqlddl.Draft{Table: table, Triggers: triggers}, actual); e != nil {
				out.Error = e
				return out
			}
		} else {
			actual, e := readObjectDefinition(qctx, c, ObjectIdentity{Database: plan.Database, Name: o.Name, Kind: strings.ToLower(o.Kind)})
			if e != nil {
				out.Error = e
				out.State = "uncertain"
				return out
			}
			wanted, e := plan.reader.DDL(i)
			if e != nil {
				out.Error = e
				return out
			}
			wanted, e = RebindBackupDDL(wanted, manifest.Database, plan.Database)
			if e != nil {
				out.Error = e
				return out
			}
			if mysqlddl.Canonical(wanted) != mysqlddl.Canonical(actual.SQL) || o.SQLMode != actual.SQLMode {
				out.Error = errors.New("restored complete object definition differs: " + o.Name)
				return out
			}
		}
	}
	out.Verified = true
	return out
}

var _ MySQLDataImportRowSource = (*backupRowSource)(nil)
var _ = io.EOF
