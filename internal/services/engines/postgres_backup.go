package engines

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/schemaddl"
	"github.com/nyakang/nyadb/internal/services/connections"
)

var postgresToolVersion = regexp.MustCompile(`PostgreSQL\) ([0-9]+)\.`)
var postgresArchiveVersion = regexp.MustCompile(`(?m)^;\s*Dumped (?:from database|by pg_dump) version: ([0-9]+)\.`)
var schemaTOC = regexp.MustCompile(`^\d+; \d+ \d+ SCHEMA `)
var createSchemaSQL = regexp.MustCompile(`(?m)^CREATE SCHEMA ("(?:[^"]|"")*"|[^\s;]+);`)

type boundedToolOutput struct {
	bytes.Buffer
	max int
}

func (b *boundedToolOutput) Write(p []byte) (int, error) {
	if b.Len()+len(p) > b.max {
		return 0, errors.New("PostgreSQL tool output exceeds safety limit")
	}
	return b.Buffer.Write(p)
}
func pgToolPath(dir, name string) (string, error) {
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	if dir != "" {
		p := filepath.Join(dir, name)
		st, e := os.Stat(p)
		if e != nil || !st.Mode().IsRegular() {
			return "", fmt.Errorf("PostgreSQL tool unavailable: %s", name)
		}
		return p, nil
	}
	p, e := exec.LookPath(name)
	if e != nil {
		return "", fmt.Errorf("PostgreSQL tool unavailable: %s", name)
	}
	return p, nil
}
func runPGTool(ctx context.Context, p connections.Profile, password, name string, args []string) ([]byte, error) {
	path, e := pgToolPath(p.PostgreSQLOptions.ToolsDirectory, name)
	if e != nil {
		return nil, e
	}
	dir, e := os.MkdirTemp("", "nyadb-pgpass-")
	if e != nil {
		return nil, e
	}
	defer os.RemoveAll(dir)
	if strings.ContainsAny(password, "\r\n") {
		return nil, errors.New("PostgreSQL tools cannot represent a multiline password in pgpass")
	}
	escape := strings.NewReplacer(`\`, `\\`, ":", `\:`)
	passfile := filepath.Join(dir, "pgpass")
	if e = os.WriteFile(passfile, []byte("*:*:*:*:"+escape.Replace(password)+"\n"), 0600); e != nil {
		return nil, e
	}
	cmd := exec.CommandContext(ctx, path, args...)
	for _, v := range os.Environ() {
		if !strings.HasPrefix(strings.ToUpper(v), "PG") {
			cmd.Env = append(cmd.Env, v)
		}
	}
	a := p.PostgreSQLOptions
	mode := p.TLSMode
	if mode == "" {
		mode = "require"
	}
	cmd.Env = append(cmd.Env, "PGPASSFILE="+passfile, "PGSSLMODE="+mode, "PGSSLROOTCERT="+a.CAFile, "PGSSLCERT="+a.CertificateFile, "PGSSLKEY="+a.KeyFile, "PGCONNECT_TIMEOUT="+strconv.Itoa(max(1, a.ConnectTimeoutSeconds)))
	stdout, stderr := &boundedToolOutput{max: 16 << 20}, &boundedToolOutput{max: 1 << 20}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if e = cmd.Run(); e != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		// Tool diagnostics may contain archived SQL/data. They stay out of UI,
		// logs and history; the exit status and operation identify the failure.
		return nil, fmt.Errorf("%s failed: %w", name, e)
	}
	return stdout.Bytes(), nil
}
func pgConnectionArgument(p connections.Profile, database string) string {
	u := url.URL{Scheme: "postgresql", Host: net.JoinHostPort(p.Host, strconv.Itoa(int(p.Port))), User: url.User(p.Username), Path: "/" + database}
	return "--dbname=" + u.String()
}
func pgBackupTools(ctx context.Context, p connections.Profile, password string, db *sql.DB) (string, string, error) {
	var version int
	if e := db.QueryRowContext(ctx, "SHOW server_version_num").Scan(&version); e != nil {
		return "", "", e
	}
	major := version / 10000
	if major < 14 || major > 18 {
		return "", "", errors.New("PostgreSQL backup supports server versions 14–18")
	}
	for _, name := range []string{"pg_dump", "pg_restore"} {
		out, e := runPGTool(ctx, p, password, name, []string{"--version"})
		if e != nil {
			return "", "", e
		}
		m := postgresToolVersion.FindSubmatch(out)
		if len(m) != 2 || string(m[1]) != strconv.Itoa(major) {
			return "", "", errors.New("PostgreSQL tools must match the server major version")
		}
	}
	return strconv.Itoa(major), strconv.Itoa(version), nil
}
func (s *Session) postgresBackupPlan(ctx context.Context, r contract.BackupRequest) (contract.BackupPlan, error) {
	p := contract.BackupPlan{Request: r, Engine: "postgresql"}
	version, server, e := pgBackupTools(ctx, s.backupProfile, s.backupPassword, s.db)
	if e != nil {
		return p, e
	}
	p.ToolVersion, p.ServerVersion = version, server
	if r.Action == "backup" {
		if r.Scope != "database" && r.Scope != "schema" && r.Scope != "tables" {
			return p, contract.ErrUnsupported
		}
		if r.Scope == "tables" && len(r.Tables) == 0 {
			return p, connections.ErrInvalid
		}
		for _, o := range r.Tables {
			if o.ProfileID != s.id || o.Catalog != s.Catalog() || o.Kind != "table" || s.check(o) != nil {
				return p, connections.ErrInvalid
			}
			if table, err := s.Structure(ctx, o); err != nil || table.Object.Kind != "table" {
				return p, errors.Join(err, contract.ErrUnsupported)
			}
			p.Objects = append(p.Objects, qualified(o))
		}
		if r.Scope != "database" {
			p.Warnings = append(p.Warnings, "部分备份不保证包含外部依赖；恢复前请检查依赖对象。")
			selected := make([]string, len(r.Tables))
			for i, table := range r.Tables {
				selected[i] = qualified(table)
			}
			encoded, _ := json.Marshal(selected)
			// Follow owned defaults, constraints, indexes and rewrite rules. A
			// table-only dump does not include their external functions/types/FKs.
			rows, e := s.db.QueryContext(ctx, `WITH RECURSIVE members(classid,objid) AS (
 SELECT d.classid,d.objid FROM pg_depend d JOIN pg_namespace n ON d.refclassid='pg_namespace'::regclass AND d.refobjid=n.oid WHERE $2='schema' AND n.nspname=$1
 UNION
 SELECT 'pg_class'::regclass::oid,to_regclass(value)::oid FROM jsonb_array_elements_text($3::jsonb) WHERE $2='tables'
 UNION
 SELECT d.classid,d.objid FROM pg_depend d JOIN members m ON d.refclassid=m.classid AND d.refobjid=m.objid WHERE d.deptype IN ('a','i')
 ) SELECT DISTINCT pg_describe_object(d.refclassid,d.refobjid,d.refobjsubid)
 FROM members m JOIN pg_depend d ON d.classid=m.classid AND d.objid=m.objid
 CROSS JOIN LATERAL pg_identify_object(d.refclassid,d.refobjid,d.refobjsubid) info
 WHERE d.deptype='n' AND NOT EXISTS(SELECT 1 FROM members included WHERE included.classid=d.refclassid AND included.objid=d.refobjid)
 AND COALESCE(info.schema,'') NOT IN ('pg_catalog','information_schema')
 AND NOT (d.refclassid='pg_namespace'::regclass AND d.refobjid=(SELECT oid FROM pg_namespace WHERE nspname=$1))
 ORDER BY 1 LIMIT 1001`, r.Target.Object.Schema, r.Scope, string(encoded))
			if e != nil {
				return p, e
			}
			for rows.Next() {
				var v string
				if e = rows.Scan(&v); e != nil {
					break
				}
				p.Warnings = append(p.Warnings, v)
			}
			if e == nil {
				e = rows.Err()
			}
			rows.Close()
			if e != nil {
				return p, e
			}
			if len(p.Warnings) > 1001 {
				return p, errors.New("dependency preview exceeds limit")
			}
		}
		p.Warnings = append(p.Warnings, "逻辑备份不包含集群角色及物理灾备信息。")
		return p, nil
	}
	if e = schemaddl.PostgreSQLIdentifier(r.Destination); e != nil {
		return p, e
	}
	p.Digest, e = backupDigest(ctx, r.Path)
	if e != nil {
		return p, e
	}
	list, e := runPGTool(ctx, s.backupProfile, s.backupPassword, "pg_restore", []string{"--list", r.Path})
	if e != nil {
		return p, e
	}
	versions := postgresArchiveVersion.FindAllSubmatch(list, -1)
	if len(versions) != 2 {
		return p, errors.New("archive version unavailable")
	}
	for _, v := range versions {
		if string(v[1]) != version {
			return p, errors.New("archive and target major versions differ")
		}
	}
	var exists, canCreate bool
	if e = s.db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM pg_database WHERE datname=$1),rolcreatedb OR rolsuper FROM pg_roles WHERE rolname=current_user", r.Destination).Scan(&exists, &canCreate); e != nil {
		return p, e
	}
	if exists && !r.Overwrite || !exists && r.Overwrite {
		return p, errors.New("restore target existence differs from chosen policy")
	}
	if !exists && !canCreate {
		return p, errors.New("creating the restore database requires CREATEDB")
	}
	var target *sql.DB
	if exists {
		if e = s.db.QueryRowContext(ctx, "SELECT oid::bigint FROM pg_database WHERE datname=$1", r.Destination).Scan(&p.TargetCatalogID); e != nil {
			return p, e
		}
		profile := s.backupProfile
		profile.Database = r.Destination
		target, e = (postgresDialect{}).open(profile, s.backupPassword, nil)
		if e != nil {
			return p, e
		}
		defer target.Close()
		target.SetMaxOpenConns(1)
	}
	for _, line := range strings.Split(string(list), "\n") {
		if line == "" || strings.HasPrefix(line, ";") {
			continue
		}
		if len(p.Objects) >= 100000 {
			return p, errors.New("archive contains too many objects")
		}
		if exists && schemaTOC.MatchString(line) {
			name, e := pgArchiveSchema(ctx, s.backupProfile, s.backupPassword, r.Path, line)
			if e != nil {
				return p, e
			}
			var present bool
			if e = target.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM pg_namespace WHERE nspname=$1)", name).Scan(&present); e != nil {
				return p, e
			}
			if present {
				p.Warnings = append(p.Warnings, "保留已有 schema: "+name)
				continue
			}
		}
		p.Objects = append(p.Objects, line)
	}
	if r.PreserveACL || r.PreserveOwner {
		if e = s.pgArchiveRoles(ctx, r.Path, target); e != nil {
			return p, e
		}
	}
	p.Warnings = append(p.Warnings, "覆盖仅替换归档对象；归档外依赖冲突会导致恢复失败，不执行级联删除。")
	return p, nil
}
func pgArchiveSchema(ctx context.Context, p connections.Profile, password, path, line string) (string, error) {
	f, e := os.CreateTemp("", "nyadb-toc-*")
	if e != nil {
		return "", e
	}
	name := f.Name()
	defer os.Remove(name)
	_, e = io.WriteString(f, line+"\n")
	f.Close()
	if e != nil {
		return "", e
	}
	out, e := runPGTool(ctx, p, password, "pg_restore", []string{"--use-list=" + name, "--schema-only", "--no-owner", "--no-privileges", "--file=-", path})
	if e != nil {
		return "", e
	}
	m := createSchemaSQL.FindSubmatch(out)
	if len(m) != 2 {
		return "", errors.New("archive schema cannot be safely identified")
	}
	s := string(m[1])
	if strings.HasPrefix(s, `"`) {
		s = strings.ReplaceAll(s[1:len(s)-1], `""`, `"`)
	}
	return s, schemaddl.PostgreSQLIdentifier(s)
}

var roleSQL = regexp.MustCompile(`(?m)^(?:ALTER .* OWNER TO |GRANT .* TO |REVOKE .* FROM )(.+?);$`)

func (s *Session) pgArchiveRoles(ctx context.Context, path string, target *sql.DB) error {
	if target == nil {
		target = s.db
	}
	out, e := runPGTool(ctx, s.backupProfile, s.backupPassword, "pg_restore", []string{"--schema-only", "--file=-", path})
	if e != nil {
		return e
	}
	for _, m := range roleSQL.FindAllSubmatch(out, -1) {
		v := strings.TrimSuffix(string(m[1]), " WITH GRANT OPTION")
		for _, role := range strings.Split(v, ",") {
			role = strings.TrimSpace(role)
			if role == "PUBLIC" {
				continue
			}
			if strings.HasPrefix(role, `"`) && strings.HasSuffix(role, `"`) {
				role = strings.ReplaceAll(role[1:len(role)-1], `""`, `"`)
			}
			if e = schemaddl.PostgreSQLIdentifier(role); e != nil {
				return e
			}
			var ok bool
			if e = target.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM pg_roles WHERE rolname=$1)", role).Scan(&ok); e != nil {
				return e
			}
			if !ok {
				return fmt.Errorf("required restore role is missing: %s", role)
			}
		}
	}
	return nil
}
func (t *backupTask) executePostgresBackup(progress func(contract.BackupProgress)) (out contract.BackupOutcome) {
	out.State = "not_executed"
	r := t.plan.Request
	p := t.profile
	db, e := (postgresDialect{}).open(p, t.password, nil)
	if e != nil {
		out.Error = e
		return
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	tool, server, e := pgBackupTools(t.ctx, p, t.password, db)
	if e != nil || tool != t.plan.ToolVersion || server != t.plan.ServerVersion {
		out.Error = errors.Join(e, errors.New("tool or server version changed since preview"))
		return
	}
	if r.Action == "backup" {
		if _, e = os.Lstat(r.Path); !os.IsNotExist(e) {
			out.Error = errors.New("backup output already exists or cannot be checked")
			return
		}
		f, e := os.CreateTemp(filepath.Dir(r.Path), ".nyadb-pg-*.dump")
		if e != nil {
			out.Error = e
			return
		}
		name := f.Name()
		f.Close()
		defer os.Remove(name)
		args := []string{"--format=custom", "--no-password", pgConnectionArgument(p, p.Database), "--file=" + name}
		if r.Scope == "schema" {
			args = append(args, "--schema="+quote(r.Target.Object.Schema))
		}
		if r.Scope == "tables" {
			for _, table := range r.Tables {
				args = append(args, "--table="+qualified(table))
			}
		}
		progress(contract.BackupProgress{Phase: "pg_dump"})
		if _, e = runPGTool(t.ctx, p, t.password, "pg_dump", args); e != nil {
			out.State, out.Error = "failed", e
			return
		}
		if _, e = runPGTool(t.ctx, p, t.password, "pg_restore", []string{"--list", name}); e != nil {
			out.State, out.Error = "failed", e
			return
		}
		if e = os.Link(name, r.Path); e != nil {
			out.Error = e
			return
		}
		out.State, out.Path = "completed", r.Path
		return
	}
	if !r.Overwrite {
		if _, e = db.ExecContext(t.ctx, "CREATE DATABASE "+quote(r.Destination)+" TEMPLATE template0"); e != nil {
			out.State, out.Path, out.Error = "uncertain", r.Destination, e
			return
		}
	} else {
		var oid int64
		if e = db.QueryRowContext(t.ctx, "SELECT oid::bigint FROM pg_database WHERE datname=$1", r.Destination).Scan(&oid); e != nil || oid != t.plan.TargetCatalogID {
			out.Error = errors.Join(e, errors.New("restore target database changed since preview"))
			return
		}
	}
	out.Path = r.Destination
	if !r.Overwrite {
		out.State = "failed"
	} // The new database already exists.
	objects := t.plan.Objects
	if !r.Overwrite {
		// template0 normally supplies public. Preserve any schema already
		// created by the template instead of failing CREATE SCHEMA halfway
		// through an otherwise valid single-transaction restore.
		targetProfile := p
		targetProfile.Database = r.Destination
		target, err := (postgresDialect{}).open(targetProfile, t.password, nil)
		if err != nil {
			out.Error = err
			return
		}
		defer target.Close()
		target.SetMaxOpenConns(1)
		objects = make([]string, 0, len(t.plan.Objects))
		for _, line := range t.plan.Objects {
			if schemaTOC.MatchString(line) {
				name, err := pgArchiveSchema(t.ctx, p, t.password, r.Path, line)
				if err != nil {
					out.Error = err
					return
				}
				var exists bool
				if err = target.QueryRowContext(t.ctx, "SELECT EXISTS(SELECT 1 FROM pg_namespace WHERE nspname=$1)", name).Scan(&exists); err != nil {
					out.Error = err
					return
				}
				if exists {
					continue
				}
			}
			objects = append(objects, line)
		}
	}
	f, e := os.CreateTemp("", "nyadb-restore-toc-*")
	if e != nil {
		out.Error = e
		return
	}
	list := f.Name()
	defer os.Remove(list)
	_, e = io.WriteString(f, strings.Join(objects, "\n")+"\n")
	f.Close()
	if e != nil {
		out.Error = e
		return
	}
	args := []string{"--no-password", "--single-transaction", "--exit-on-error", "--use-list=" + list, pgConnectionArgument(p, r.Destination)}
	if r.Overwrite {
		args = append(args, "--clean", "--if-exists")
	}
	if !r.PreserveOwner {
		args = append(args, "--no-owner")
	}
	if !r.PreserveACL {
		args = append(args, "--no-privileges")
	}
	args = append(args, r.Path)
	progress(contract.BackupProgress{Phase: "pg_restore"})
	_, e = runPGTool(t.ctx, p, t.password, "pg_restore", args)
	out.Path = r.Destination
	if e != nil {
		out.State, out.Error = "uncertain", e
		return
	}
	out.State = "completed"
	return
}
