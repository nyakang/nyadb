package dameng

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/dameng/dpi"
)

const metadataLimit = 50000

func validName(v string) bool {
	return v != "" && len(v) <= 128 && utf8.ValidString(v) && strings.IndexFunc(v, unicode.IsControl) < 0
}
func quote(v string) string { return `"` + strings.ReplaceAll(v, `"`, `""`) + `"` }
func (s *Session) Namespace(schema string) contract.NamespaceRef {
	return contract.NamespaceRef{ProfileID: s.ProfileID(), Catalog: s.Catalog(), Schema: schema}
}
func (s *Session) checkNamespace(n contract.NamespaceRef) error {
	if !s.Alive() {
		return contract.ErrRevoked
	}
	if n.ProfileID != s.ProfileID() || n.Catalog != s.Catalog() || !validName(n.Schema) {
		return connections.ErrInvalid
	}
	return nil
}
func (s *Session) checkObject(o contract.ObjectRef) error {
	if err := s.checkNamespace(o.Namespace()); err != nil {
		return err
	}
	if !validName(o.Name) {
		return connections.ErrInvalid
	}
	if _, ok := objectLabels[strings.ToUpper(o.Kind)]; !ok {
		return contract.ErrUnsupported
	}
	return nil
}

var objectLabels = map[string]string{"TABLE": "表", "VIEW": "视图", "PROCEDURE": "存储过程", "FUNCTION": "函数", "PACKAGE": "包", "TRIGGER": "触发器", "SEQUENCE": "序列", "TYPE": "类型"}

// Native ALL_OBJECTS represents a schema as OBJECT_TYPE=SCH and OBJECT_NAME,
// not its owner. UNION owners keeps granted foreign objects discoverable even
// when a non-DBA cannot enumerate SYSOBJECTS or another user's ALL_USERS row.
const namespacesSQL = `SELECT NAME FROM (SELECT OBJECT_NAME AS NAME FROM SYS.ALL_OBJECTS WHERE OBJECT_TYPE='SCH' UNION SELECT OWNER AS NAME FROM SYS.ALL_OBJECTS WHERE OBJECT_TYPE IN ('TABLE','VIEW','PROCEDURE','FUNCTION','PACKAGE','TRIGGER','SEQUENCE','TYPE')) ORDER BY NAME LIMIT 50001`

func (s *Session) Namespaces(ctx context.Context) ([]contract.Namespace, error) {
	ctx, done, err := s.request(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	rows, err := s.db.QueryContext(ctx, namespacesSQL)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	preferred := s.profile.DamengOptions.DefaultSchema
	if preferred == "" {
		preferred = s.profile.Username
	}
	out := []contract.Namespace{{Ref: s.Namespace(preferred), Label: preferred}}
	seen := map[string]bool{preferred: true}
	count := 0
	for rows.Next() {
		count++
		var name string
		if err = rows.Scan(&name); err != nil {
			return nil, err
		}
		if !validName(name) || count > metadataLimit {
			return nil, contract.ErrUnsupported
		}
		if seen[name] {
			continue
		}
		switch name {
		case "SYS", "SYSAUDITOR", "SYSSSO", "CTISYS":
			continue
		}
		seen[name] = true
		out = append(out, contract.Namespace{Ref: s.Namespace(name), Label: name})
	}
	return out, errors.Join(rows.Err(), rows.Close(), ctx.Err())
}

const browseSQL = `SELECT O.OBJECT_NAME,O.OBJECT_TYPE,C.COMMENTS,O.STATUS,TO_CHAR(O.CREATED,'YYYY-MM-DD"T"HH24:MI:SS'),TO_CHAR(O.LAST_DDL_TIME,'YYYY-MM-DD"T"HH24:MI:SS') FROM SYS.ALL_OBJECTS O LEFT JOIN SYS.ALL_TAB_COMMENTS C ON C.OWNER=O.OWNER AND C.TABLE_NAME=O.OBJECT_NAME WHERE O.OWNER=? AND O.SUBOBJECT_NAME IS NULL AND O.OBJECT_TYPE IN ('TABLE','VIEW','PROCEDURE','FUNCTION','PACKAGE','TRIGGER','SEQUENCE','TYPE') ORDER BY O.OBJECT_NAME,O.OBJECT_TYPE LIMIT 50001`

func (s *Session) Browse(ctx context.Context, n contract.NamespaceRef) ([]contract.DBObject, error) {
	if err := s.checkNamespace(n); err != nil {
		return nil, err
	}
	ctx, done, err := s.request(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	rows, err := s.db.QueryContext(ctx, browseSQL, n.Schema)
	if err != nil {
		return nil, err
	}
	var out []contract.DBObject
	for rows.Next() {
		var o contract.DBObject
		var typ string
		var comment, status, created, modified sql.NullString
		if err = rows.Scan(&o.Name, &typ, &comment, &status, &created, &modified); err != nil {
			rows.Close()
			return nil, err
		}
		if !validName(o.Name) || objectLabels[typ] == "" || len(out) >= metadataLimit {
			rows.Close()
			return nil, contract.ErrUnsupported
		}
		o.Ref, o.Kind, o.Engine = n.Object(strings.ToLower(typ), o.Name), objectLabels[typ], "Dameng"
		o.Comment, o.Status, o.CreatedAt, o.ModifiedAt = comment.String, status.String, created.String, modified.String
		out = append(out, o)
	}
	err = errors.Join(rows.Err(), rows.Close())
	if err != nil {
		return nil, err
	}
	// Close the object cursor before starting source/DDL reads so a one-slot
	// background pool remains usable. Definitions preserve the native text.
	rows, err = s.db.QueryContext(ctx, `SELECT NAME,TYPE,LINE,TEXT FROM SYS.ALL_SOURCE WHERE OWNER=? ORDER BY NAME,TYPE,LINE LIMIT 50001`, n.Schema)
	if err != nil {
		return nil, err
	}
	source := make(map[string]*strings.Builder)
	previous := make(map[string]sql.NullInt64)
	count, total := 0, 0
	for rows.Next() {
		var name, typ string
		var text sql.NullString
		var line sql.NullInt64
		if err = rows.Scan(&name, &typ, &line, &text); err != nil {
			break
		}
		count++
		total += len(text.String)
		key := name + "\x00" + typ
		prior, exists := previous[key]
		if !validName(name) || count > metadataLimit || total > 16<<20 || line.Valid && line.Int64 < 0 || exists && (!line.Valid || !prior.Valid || line.Int64 <= prior.Int64) {
			err = contract.ErrUnsupported
			break
		}
		previous[key] = line
		if !text.Valid {
			continue
		}
		if source[key] == nil {
			source[key] = new(strings.Builder)
		}
		if source[key].Len()+len(text.String) > 1<<20 {
			err = contract.ErrUnsupported
			break
		}
		source[key].WriteString(text.String)
	}
	err = errors.Join(err, rows.Err(), rows.Close())
	if err != nil {
		return nil, err
	}
	definitionBytes := 0
	for i := range out {
		o := &out[i]
		key := o.Name + "\x00" + strings.ToUpper(o.Ref.Kind)
		text := source[key]
		if text == nil && (o.Ref.Kind == "procedure" || o.Ref.Kind == "function") {
			text = source[o.Name+"\x00PROC"]
		}
		if text != nil {
			o.Definition = text.String()
			if body := source[key+" BODY"]; body != nil {
				o.Definition += "\n/\n" + body.String()
			}
		}
		if o.Ref.Kind == "sequence" || o.Ref.Kind == "view" {
			o.Definition, err = s.definition(ctx, o.Ref)
			if err != nil {
				// Accessible metadata need not grant native source/DDL access.
				// A transport failure or cancellation is never a missing definition.
				if !metadataDenied(err) {
					return nil, err
				}
			}
		}
		definitionBytes += len(o.Definition)
		if definitionBytes > 16<<20 {
			return nil, contract.ErrUnsupported
		}
	}
	return out, ctx.Err()
}

func metadataDenied(err error) bool {
	var native *dpi.Error
	return errors.As(err, &native) && (native.Code == -5514 || native.Code == -20008)
}

func (s *Session) definition(ctx context.Context, o contract.ObjectRef) (string, error) {
	return definitionAt(ctx, s.db, o)
}

type metadataQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func definitionAt(ctx context.Context, q metadataQueryer, o contract.ObjectRef) (string, error) {
	var text string
	err := q.QueryRowContext(ctx, `SELECT SYS.DBMS_METADATA.GET_DDL(?,?,?)`, strings.ToUpper(o.Kind), o.Name, o.Schema).Scan(&text)
	if err == nil && (len(text) > 1<<20 || !utf8.ValidString(text)) {
		return "", contract.ErrUnsupported
	}
	return text, err
}

func (s *Session) TableMetadata(ctx context.Context, o contract.ObjectRef) (*contract.TableStructure, error) {
	if err := s.checkObject(o); err != nil {
		return nil, err
	}
	if o.Kind != "table" && o.Kind != "view" {
		return nil, contract.ErrUnsupported
	}
	ctx, done, err := s.request(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	return s.structure(ctx, o)
}
