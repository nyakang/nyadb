package engines

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func cloudberryLiteral(value string) string { return "'" + strings.ReplaceAll(value, "'", "''") + "'" }

func cloudberryOptionClauses(options []string, assignment bool) (string, error) {
	var clauses []string
	separator := " "
	if assignment {
		separator = "="
	}
	for _, option := range options {
		name, value, ok := strings.Cut(option, "=")
		if !ok || name == "" || !utf8.ValidString(option) || strings.ContainsRune(option, 0) {
			return "", errors.New("invalid Cloudberry native option")
		}
		clauses = append(clauses, quote(name)+separator+cloudberryLiteral(value))
	}
	return strings.Join(clauses, ", "), nil
}

// Read definitions directly from the server catalog when the optional native
// helper is absent. Stored expressions are server-rendered in pg_catalog's
// search path; names and values are quoted independently, never split by lines.
func (s *Session) cloudberryDefinition(ctx context.Context, q queryer, o contract.ObjectRef, info *contract.TableStructure) (string, error) {
	ddl, available, err := cloudberryNativeDefinition(ctx, q, o)
	if err != nil || available {
		return ddl, err
	}
	var kind, persistence, partitionKey, bound, parentsJSON, tablespace string
	var partition, rowSecurity, forceSecurity bool
	err = queryOne(ctx, q, `SELECT c.relkind::text,c.relpersistence::text,c.relispartition,
 COALESCE(pg_get_partkeydef(c.oid),''),COALESCE(pg_get_expr(c.relpartbound,c.oid,true),''),
 COALESCE((SELECT json_agg(format('%I.%I',pn.nspname,pc.relname) ORDER BY i.inhseqno)::text
 FROM pg_inherits i JOIN pg_class pc ON pc.oid=i.inhparent JOIN pg_namespace pn ON pn.oid=pc.relnamespace
 WHERE i.inhrelid=c.oid),'[]'),COALESCE(ts.spcname,''),c.relrowsecurity,c.relforcerowsecurity
 FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace LEFT JOIN pg_tablespace ts ON ts.oid=c.reltablespace
 WHERE n.nspname=$1 AND c.relname=$2`, []any{o.Schema, o.Name}, &kind, &persistence, &partition, &partitionKey, &bound, &parentsJSON, &tablespace, &rowSecurity, &forceSecurity)
	if err != nil {
		return "", err
	}
	var parents []string
	if len(parentsJSON) > 256<<10 || json.Unmarshal([]byte(parentsJSON), &parents) != nil || parents == nil || len(parents) > 1000 || partition && (len(parents) != 1 || bound == "") {
		return "", errors.New("incomplete Cloudberry inheritance metadata")
	}
	sequences, err := cloudberryOwnedSequences(ctx, q, o)
	if err != nil {
		return "", err
	}
	byColumn := map[string]cloudberrySequence{}
	var before, after []string
	for _, sequence := range sequences {
		byColumn[sequence.column] = sequence
		if sequence.identity == "" {
			before = append(before, "CREATE SEQUENCE "+sequence.name+" AS "+sequence.dataType+" "+sequence.options()+";")
			after = append(after, "ALTER SEQUENCE "+sequence.name+" OWNED BY "+qualified(o)+"."+quote(sequence.column)+";")
		}
	}
	rows, err := q.QueryContext(ctx, `SELECT a.attname,format_type(a.atttypid,a.atttypmod),a.attnotnull,
 a.attgenerated::text,a.attidentity::text,COALESCE(pg_get_expr(d.adbin,d.adrelid,true),''),
 CASE WHEN a.attcollation<>0 AND a.attcollation<>ty.typcollation THEN format('%I.%I',cn.nspname,coll.collname) ELSE '' END,
 COALESCE(array_to_json(enc.attoptions)::text,'[]'),a.attislocal
 FROM pg_attribute a JOIN pg_class c ON c.oid=a.attrelid JOIN pg_namespace n ON n.oid=c.relnamespace
 JOIN pg_type ty ON ty.oid=a.atttypid LEFT JOIN pg_attrdef d ON d.adrelid=a.attrelid AND d.adnum=a.attnum
 LEFT JOIN pg_collation coll ON coll.oid=a.attcollation LEFT JOIN pg_namespace cn ON cn.oid=coll.collnamespace
 LEFT JOIN pg_attribute_encoding enc ON enc.attrelid=a.attrelid AND enc.attnum=a.attnum
 WHERE n.nspname=$1 AND c.relname=$2 AND a.attnum>0 AND NOT a.attisdropped ORDER BY a.attnum`, o.Schema, o.Name)
	if err != nil {
		return "", err
	}
	var columns []string
	columnBytes := 0
	for rows.Next() {
		var name, dataType, generated, identity, expression, collation, encoding string
		var notNull, local bool
		if err = rows.Scan(&name, &dataType, &notNull, &generated, &identity, &expression, &collation, &encoding, &local); err != nil {
			break
		}
		if len(columns) >= 10000 {
			err = errors.New("Cloudberry field definition limit exceeded")
			break
		}
		if len(parents) != 0 && !partition && !local {
			continue // INHERITS restores these columns and their inherited constraints.
		}
		column := quote(name) + " " + dataType
		if collation != "" {
			column += " COLLATE " + collation
		}
		if identity != "" {
			sequence, ok := byColumn[name]
			mode := map[string]string{"a": "ALWAYS", "d": "BY DEFAULT"}[identity]
			if !ok || sequence.identity != identity || mode == "" || expression != "" || generated != "" {
				err = errors.New("incomplete Cloudberry identity sequence definition")
				break
			}
			column += " GENERATED " + mode + " AS IDENTITY (SEQUENCE NAME " + sequence.name + " " + sequence.options() + ")"
		}
		if generated != "" {
			if generated != "s" || expression == "" {
				err = errors.New("unknown Cloudberry generated field semantics")
				break
			}
			column += " GENERATED ALWAYS AS (" + expression + ") STORED"
		} else if expression != "" && identity == "" {
			column += " DEFAULT " + expression
		}
		if notNull {
			column += " NOT NULL"
		}
		options, e := cloudberryOptions(encoding)
		if e != nil {
			err = e
			break
		}
		if len(options) != 0 {
			clauses, e := cloudberryOptionClauses(options, true)
			if e != nil {
				err = e
				break
			}
			column += " ENCODING (" + clauses + ")"
		}
		columns = append(columns, column)
		columnBytes += len(column)
		if columnBytes > 1<<20 {
			err = errors.New("Cloudberry field definitions exceed 1 MiB")
			break
		}
	}
	err = errors.Join(err, rows.Err())
	rows.Close()
	if err != nil {
		return "", err
	}
	constraints, err := cloudberryDefinitionRows(ctx, q, `SELECT 'CONSTRAINT '||quote_ident(con.conname)||' '||pg_get_constraintdef(con.oid,true)
 FROM pg_constraint con JOIN pg_class c ON c.oid=con.conrelid JOIN pg_namespace n ON n.oid=c.relnamespace
 WHERE n.nspname=$1 AND c.relname=$2 AND con.conislocal ORDER BY con.conname`, o)
	if err != nil {
		return "", err
	}
	columns = append(columns, constraints...)
	if len(columns) == 0 && len(parents) == 0 {
		return "", errors.New("Cloudberry table fields unavailable")
	}
	header := "CREATE TABLE "
	if kind == "f" {
		header = "CREATE FOREIGN TABLE "
	} else if persistence == "u" {
		header = "CREATE UNLOGGED TABLE "
	} else if persistence != "p" {
		return "", errors.New("temporary Cloudberry definitions are session scoped")
	}
	ddl = header + qualified(o) + " (\n  " + strings.Join(columns, ",\n  ") + "\n)"
	if !partition && len(parents) != 0 {
		ddl += "\nINHERITS (" + strings.Join(parents, ", ") + ")"
	}
	if partitionKey != "" {
		ddl += "\nPARTITION BY " + partitionKey
	}
	storage, err := cloudberryStorageClauses(info)
	if err != nil {
		return "", err
	}
	ddl += storage
	if tablespace != "" {
		ddl += "\nTABLESPACE " + quote(tablespace)
	}
	if info.Distribution != "" {
		ddl += "\n" + info.Distribution
	}
	ddl += ";"
	if len(before) != 0 {
		ddl = strings.Join(before, "\n") + "\n" + ddl
	}
	if len(after) != 0 {
		ddl += "\n" + strings.Join(after, "\n")
	}
	if partition {
		ddl += "\nALTER TABLE " + parents[0] + " ATTACH PARTITION " + qualified(o) + " " + bound + ";"
	}
	indexes, err := cloudberryDefinitionRows(ctx, q, `SELECT pg_get_indexdef(i.indexrelid)
 FROM pg_index i JOIN pg_class c ON c.oid=i.indrelid JOIN pg_namespace n ON n.oid=c.relnamespace
 WHERE n.nspname=$1 AND c.relname=$2 AND NOT EXISTS(SELECT 1 FROM pg_constraint con WHERE con.conindid=i.indexrelid) ORDER BY i.indexrelid`, o)
	if err != nil {
		return "", err
	}
	for _, index := range indexes {
		ddl += "\n" + index + ";"
	}
	if info.Comment != "" {
		ddl += "\nCOMMENT ON TABLE " + qualified(o) + " IS " + cloudberryLiteral(info.Comment) + ";"
	}
	for _, column := range info.Columns {
		if column.Comment != "" {
			ddl += "\nCOMMENT ON COLUMN " + qualified(o) + "." + quote(column.Name) + " IS " + cloudberryLiteral(column.Comment) + ";"
		}
	}
	// Retain user rules and triggers; these affect actual writes independently
	// of columns and constraints and cannot be inferred from generic metadata.
	rules, err := cloudberryDefinitionRows(ctx, q, `SELECT pg_get_ruledef(r.oid,true) FROM pg_rewrite r JOIN pg_class c ON c.oid=r.ev_class JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=$1 AND c.relname=$2 AND r.rulename<>'_RETURN' ORDER BY r.rulename`, o)
	if err != nil {
		return "", err
	}
	triggers, err := cloudberryDefinitionRows(ctx, q, `SELECT pg_get_triggerdef(t.oid,true)||'; ALTER TABLE '||format('%I.%I',n.nspname,c.relname)||
 CASE t.tgenabled WHEN 'D' THEN ' DISABLE TRIGGER ' WHEN 'R' THEN ' ENABLE REPLICA TRIGGER ' WHEN 'A' THEN ' ENABLE ALWAYS TRIGGER ' ELSE ' ENABLE TRIGGER ' END||quote_ident(t.tgname)
 FROM pg_trigger t JOIN pg_class c ON c.oid=t.tgrelid JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=$1 AND c.relname=$2 AND NOT t.tgisinternal ORDER BY t.tgname`, o)
	if err != nil {
		return "", err
	}
	for _, definition := range append(rules, triggers...) {
		ddl += "\n" + strings.TrimSuffix(definition, ";") + ";"
	}
	policies, err := cloudberryDefinitionRows(ctx, q, `SELECT 'CREATE POLICY '||quote_ident(p.polname)||' ON '||format('%I.%I',n.nspname,c.relname)||
 CASE WHEN p.polpermissive THEN ' AS PERMISSIVE' ELSE ' AS RESTRICTIVE' END||
 CASE p.polcmd WHEN '*' THEN ' FOR ALL' WHEN 'r' THEN ' FOR SELECT' WHEN 'a' THEN ' FOR INSERT' WHEN 'w' THEN ' FOR UPDATE' WHEN 'd' THEN ' FOR DELETE' END||
 ' TO '||(SELECT string_agg(CASE WHEN role_id=0 THEN 'PUBLIC' ELSE quote_ident(pg_get_userbyid(role_id)) END,', ' ORDER BY ord) FROM unnest(p.polroles) WITH ORDINALITY r(role_id,ord))||
 CASE WHEN p.polqual IS NOT NULL THEN ' USING ('||pg_get_expr(p.polqual,p.polrelid,true)||')' ELSE '' END||
 CASE WHEN p.polwithcheck IS NOT NULL THEN ' WITH CHECK ('||pg_get_expr(p.polwithcheck,p.polrelid,true)||')' ELSE '' END
 FROM pg_policy p JOIN pg_class c ON c.oid=p.polrelid JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=$1 AND c.relname=$2 ORDER BY p.polname`, o)
	if err != nil {
		return "", err
	}
	for _, policy := range policies {
		ddl += "\n" + policy + ";"
	}
	if rowSecurity {
		ddl += "\nALTER TABLE " + qualified(o) + " ENABLE ROW LEVEL SECURITY;"
	}
	if forceSecurity {
		ddl += "\nALTER TABLE " + qualified(o) + " FORCE ROW LEVEL SECURITY;"
	}
	if len(ddl) > 1<<20 || !utf8.ValidString(ddl) || strings.ContainsRune(ddl, 0) {
		return "", errors.New("complete Cloudberry table definition exceeds 1 MiB")
	}
	return ddl, ctx.Err()
}

type cloudberrySequence struct {
	column, identity, name, dataType  string
	start, increment, min, max, cache int64
	cycle                             bool
}

func (s cloudberrySequence) options() string {
	cycle := "NO CYCLE"
	if s.cycle {
		cycle = "CYCLE"
	}
	return fmt.Sprintf("START WITH %d INCREMENT BY %d MINVALUE %d MAXVALUE %d CACHE %d %s", s.start, s.increment, s.min, s.max, s.cache, cycle)
}

func cloudberryOwnedSequences(ctx context.Context, q queryer, o contract.ObjectRef) ([]cloudberrySequence, error) {
	rows, err := q.QueryContext(ctx, `SELECT a.attname,a.attidentity::text,format('%I.%I',sn.nspname,seq.relname),format_type(settings.seqtypid,NULL),
 settings.seqstart,settings.seqincrement,settings.seqmin,settings.seqmax,settings.seqcache,settings.seqcycle
 FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
 JOIN pg_depend dep ON dep.refclassid='pg_class'::regclass AND dep.refobjid=c.oid AND dep.classid='pg_class'::regclass AND dep.deptype IN ('a','i')
 JOIN pg_class seq ON seq.oid=dep.objid AND seq.relkind='S' JOIN pg_namespace sn ON sn.oid=seq.relnamespace
 JOIN pg_sequence settings ON settings.seqrelid=seq.oid JOIN pg_attribute a ON a.attrelid=c.oid AND a.attnum=dep.refobjsubid AND NOT a.attisdropped
 WHERE n.nspname=$1 AND c.relname=$2 ORDER BY a.attnum`, o.Schema, o.Name)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []cloudberrySequence
	seen := map[string]bool{}
	for rows.Next() {
		var sequence cloudberrySequence
		if err = rows.Scan(&sequence.column, &sequence.identity, &sequence.name, &sequence.dataType, &sequence.start, &sequence.increment, &sequence.min, &sequence.max, &sequence.cache, &sequence.cycle); err != nil {
			return nil, err
		}
		if len(out) >= 10000 || seen[sequence.column] || sequence.increment == 0 || sequence.cache <= 0 {
			return nil, errors.New("invalid or ambiguous Cloudberry sequence ownership")
		}
		seen[sequence.column] = true
		out = append(out, sequence)
	}
	return out, rows.Err()
}

func cloudberryStorageClauses(info *contract.TableStructure) (string, error) {
	var clauses string
	if info.Kind == "外部表" {
		if info.ExternalServer == "" {
			return "", errors.New("Cloudberry external server is unavailable")
		}
		clauses = "\nSERVER " + quote(info.ExternalServer)
		if len(info.ExternalOptions) != 0 {
			options, err := cloudberryOptionClauses(info.ExternalOptions, false)
			if err != nil {
				return "", err
			}
			clauses += "\nOPTIONS (" + options + ")"
		}
		return clauses, nil
	}
	if info.Engine != "" {
		clauses += "\nUSING " + quote(info.Engine)
	}
	if len(info.StorageOptions) != 0 {
		options, err := cloudberryOptionClauses(info.StorageOptions, true)
		if err != nil {
			return "", err
		}
		clauses += "\nWITH (" + options + ")"
	}
	return clauses, nil
}

func cloudberryDefinitionRows(ctx context.Context, q queryer, query string, o contract.ObjectRef) ([]string, error) {
	rows, err := q.QueryContext(ctx, query, o.Schema, o.Name)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var definitions []string
	bytes := 0
	for rows.Next() {
		var value sql.NullString
		if err = rows.Scan(&value); err != nil {
			return nil, err
		}
		bytes += len(value.String)
		if !value.Valid || value.String == "" || len(definitions) >= 10000 || bytes > 1<<20 || !utf8.ValidString(value.String) || strings.ContainsRune(value.String, 0) {
			return nil, errors.New("incomplete or oversized Cloudberry catalog definition")
		}
		definitions = append(definitions, value.String)
	}
	return definitions, rows.Err()
}
