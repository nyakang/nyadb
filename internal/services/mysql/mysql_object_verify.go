package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode"

	"github.com/nyakang/nyadb/internal/db/sqltext"
)

// ObjectVerificationContext is read on the same physical connection that
// acknowledged DDL. It identifies the server defaults we can safely compare.
type ObjectVerificationContext struct {
	SQLMode, CurrentUser                                   string
	DatabaseCharset, DatabaseCollation, DatabaseEncryption string
	EventDefaultStart                                      string
}

// ObjectDefinitionEquivalent conservatively compares complete public DDL.
// Unrecognized normalization is a verification failure, never assumed equal.
func ObjectDefinitionEquivalent(p ObjectPlan, actual ObjectDefinition, execution ObjectVerificationContext) error {
	fail := func() error {
		return errors.New("DDL acknowledged, but the reloaded complete object definition differs or cannot be verified; draft retained")
	}
	kind, e := objectKind(p.Original.Kind)
	if e != nil {
		return e
	}
	actualKind, e := objectKind(actual.Kind)
	if e != nil || kind != actualKind || actual.Database != p.Original.Database || actual.Name != p.Original.Name {
		return fail()
	}
	if kind == "FUNCTION" || kind == "PROCEDURE" || kind == "EVENT" {
		if !actual.SQLModeKnown {
			return fail()
		}
		if p.Original.SQLModeKnown && normalizeObjectSQLMode(p.Original.SQLMode) != normalizeObjectSQLMode(execution.SQLMode) {
			return fail()
		}
		if normalizeObjectSQLMode(actual.SQLMode) != normalizeObjectSQLMode(execution.SQLMode) {
			return fail()
		}
	}
	if strings.Contains(strings.ToUpper(execution.SQLMode), "NO_BACKSLASH_ESCAPES") || strings.Contains(strings.ToUpper(execution.SQLMode), "ANSI_QUOTES") {
		return errors.New("reloaded object verification does not support this session quote mode; draft retained")
	}
	want, e := parseVerifiedObject(p.SQL, kind, p.Original.ObjectIdentity)
	if e != nil {
		return fail()
	}
	got, e := parseVerifiedObject(actual.SQL, kind, p.Original.ObjectIdentity)
	if e != nil {
		return fail()
	}
	if kind == "DATABASE" {
		if execution.DatabaseCharset == "" || execution.DatabaseCollation == "" || execution.DatabaseEncryption == "" {
			return fail()
		}
		expected := map[string]string{"charset": execution.DatabaseCharset, "collation": execution.DatabaseCollation, "encryption": execution.DatabaseEncryption}
		for k, value := range want.options {
			if expected[k] != value {
				return fail()
			}
		}
		for k, v := range expected {
			if got.options[k] != v {
				return fail()
			}
		}
		if len(got.options) != len(expected) {
			return fail()
		}
		return nil
	}

	expectedDefiner := want.definer
	if expectedDefiner == "" || expectedDefiner == "CURRENT_USER" {
		expectedDefiner = execution.CurrentUser
	}
	if expectedDefiner == "" || got.definer != expectedDefiner {
		return fail()
	}
	switch kind {
	case "FUNCTION", "PROCEDURE":
		if verifiedTokens(want.body) != verifiedTokens(got.body) {
			return fail()
		}
	case "VIEW":
		algorithm := want.options["algorithm"]
		if algorithm == "" {
			algorithm = "UNDEFINED"
		}
		security := want.options["security"]
		if security == "" {
			security = "DEFINER"
		}
		if got.options["algorithm"] != algorithm || got.options["security"] != security || got.options["columns"] != want.options["columns"] || verifiedTokens(want.body) != verifiedTokens(got.body) {
			return fail()
		}
	case "EVENT":
		expected := want.options
		body := want.body
		if p.Action == "alter" {
			original, e := parseVerifiedObject(p.Original.SQL, kind, p.Original.ObjectIdentity)
			if e != nil {
				return fail()
			}
			expected = copyVerifiedOptions(original.options)
			if _, changed := want.options["schedule"]; changed {
				delete(expected, "starts")
				delete(expected, "ends")
			}
			for k, v := range want.options {
				expected[k] = v
			}
			if len(body) == 0 {
				body = original.body
			}
		}
		if _, ok := expected["completion"]; !ok {
			expected["completion"] = "NOT PRESERVE"
		}
		if _, ok := expected["status"]; !ok {
			expected["status"] = "ENABLE"
		}
		if verifiedTokens(body) != verifiedTokens(got.body) {
			return fail()
		}
		for k, v := range expected {
			if got.options[k] != v {
				return fail()
			}
		}
		for k, v := range got.options {
			if _, ok := expected[k]; !ok {
				if k == "starts" && strings.HasPrefix(expected["schedule"], "EVERY:") && execution.EventDefaultStart != "" && v == execution.EventDefaultStart {
					continue
				}
				if k == "comment" && v == verifiedTokens([]sqltext.Token{{Text: "''", Quoted: true}}) {
					continue
				}
				return fail()
			}
		}
	}
	return nil
}
func normalizeObjectSQLMode(mode string) string {
	parts := strings.Split(strings.ToUpper(mode), ",")
	set := map[string]bool{}
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			set[p] = true
		}
	}
	parts = parts[:0]
	for p := range set {
		parts = append(parts, p)
	}
	sort.Strings(parts)
	return strings.Join(parts, ",")
}

type verifiedObject struct {
	definer string
	options map[string]string
	body    []sqltext.Token
}

func copyVerifiedOptions(v map[string]string) map[string]string {
	out := map[string]string{}
	for k, s := range v {
		out[k] = s
	}
	return out
}
func verificationTokens(raw string, expandDatabaseComments bool) ([]sqltext.Token, error) {
	ts, e := sqltext.Tokens(raw)
	if e != nil {
		return nil, e
	}
	var out []sqltext.Token
	for _, t := range ts {
		if t.Comment && strings.HasPrefix(t.Text, "/*!") {
			if !expandDatabaseComments {
				return nil, errors.New("unrecognized executable comment in object definition")
			}
			inner := strings.TrimSuffix(strings.TrimPrefix(t.Text, "/*!"), "*/")
			inner = strings.TrimLeftFunc(inner, func(r rune) bool { return unicode.IsDigit(r) || unicode.IsSpace(r) })
			expanded, e := sqltext.Tokens(inner)
			if e != nil {
				return nil, e
			}
			out = append(out, expanded...)
		} else {
			out = append(out, t)
		}
	}
	for len(out) > 0 && out[0].Comment {
		out = out[1:]
	}
	if len(out) > 0 && out[len(out)-1].Text == ";" && !out[len(out)-1].Quoted {
		out = out[:len(out)-1]
	}
	return out, nil
}
func parseVerifiedObject(raw, kind string, ref ObjectIdentity) (verifiedObject, error) {
	v := verifiedObject{options: map[string]string{}}
	ts, e := verificationTokens(raw, kind == "DATABASE")
	if e != nil {
		return v, e
	}
	if len(ts) < 3 || (!sqltext.Word(ts[0], "CREATE") && !sqltext.Word(ts[0], "ALTER")) {
		return v, errors.New("incomplete public definition")
	}
	i := 1
	for i < len(ts) && !sqltext.Word(ts[i], kind) {
		switch {
		case sqltext.Word(ts[i], "OR") && i+1 < len(ts) && sqltext.Word(ts[i+1], "REPLACE"):
			i += 2
		case sqltext.Word(ts[i], "ALGORITHM") && kind == "VIEW" && i+2 < len(ts) && ts[i+1].Text == "=":
			v.options["algorithm"] = strings.ToUpper(ts[i+2].Text)
			i += 3
		case sqltext.Word(ts[i], "DEFINER") && i+2 < len(ts) && ts[i+1].Text == "=" && sqltext.Word(ts[i+2], "CURRENT_USER"):
			v.definer = "CURRENT_USER"
			i += 3
			if i+1 < len(ts) && ts[i].Text == "(" && ts[i+1].Text == ")" {
				i += 2
			}
		case sqltext.Word(ts[i], "DEFINER") && i+4 < len(ts) && ts[i+1].Text == "=" && ts[i+3].Text == "@":
			user, e := verifiedAccountPart(ts[i+2])
			if e != nil {
				return v, e
			}
			host, e := verifiedAccountPart(ts[i+4])
			if e != nil {
				return v, e
			}
			v.definer = user + "@" + host
			i += 5
		case sqltext.Word(ts[i], "SQL") && kind == "VIEW" && i+2 < len(ts) && sqltext.Word(ts[i+1], "SECURITY"):
			v.options["security"] = strings.ToUpper(ts[i+2].Text)
			i += 3
		default:
			return v, errors.New("unknown public object header")
		}
	}
	if i >= len(ts) {
		return v, errors.New("missing object kind")
	}
	i++
	if i >= len(ts) {
		return v, errors.New("missing object identity")
	}
	name := unquoteObjectToken(ts[i])
	i++
	database := ref.Database
	if i < len(ts) && ts[i].Text == "." {
		database = name
		i++
		if i >= len(ts) {
			return v, errors.New("missing qualified name")
		}
		name = unquoteObjectToken(ts[i])
		i++
	}
	want := ref.Name
	if kind == "DATABASE" {
		want = ref.Database
	}
	if name != want || database != ref.Database {
		return v, errors.New("public definition identity changed")
	}
	rest := ts[i:]
	switch kind {
	case "DATABASE":
		return parseVerifiedDatabase(v, rest)
	case "FUNCTION", "PROCEDURE":
		v.body = rest
		return v, nil
	case "VIEW":
		if len(rest) == 0 {
			return v, errors.New("missing view query")
		}
		if !sqltext.Word(rest[0], "AS") {
			if rest[0].Text != "(" {
				return v, errors.New("unsupported view column header")
			}
			depth := 0
			j := 0
			for ; j < len(rest); j++ {
				if !rest[j].Quoted {
					if rest[j].Text == "(" {
						depth++
					}
					if rest[j].Text == ")" {
						depth--
						if depth == 0 {
							j++
							break
						}
					}
				}
			}
			if j >= len(rest) || !sqltext.Word(rest[j], "AS") {
				return v, errors.New("missing view AS")
			}
			v.options["columns"] = verifiedTokens(rest[:j])
			rest = rest[j:]
		}
		v.body = rest[1:]
		return v, nil
	case "EVENT":
		return parseVerifiedEvent(v, rest)
	}
	return v, errors.New("unsupported public definition")
}
func verifiedAccountPart(t sqltext.Token) (string, error) {
	s := t.Text
	if !t.Quoted {
		return s, nil
	}
	if len(s) < 2 {
		return "", errors.New("bad definer")
	}
	q := s[0]
	s = s[1 : len(s)-1]
	if strings.Contains(s, "\\") {
		return "", errors.New("escaped definer requires unsupported normalization")
	}
	return strings.ReplaceAll(s, string([]byte{q, q}), string(q)), nil
}
func parseVerifiedDatabase(v verifiedObject, ts []sqltext.Token) (verifiedObject, error) {
	for i := 0; i < len(ts); {
		if sqltext.Word(ts[i], "DEFAULT") {
			i++
			continue
		}
		key := ""
		switch {
		case sqltext.Word(ts[i], "CHARACTER") && i+1 < len(ts) && sqltext.Word(ts[i+1], "SET"):
			key = "charset"
			i += 2
		case sqltext.Word(ts[i], "CHARSET"):
			key = "charset"
			i++
		case sqltext.Word(ts[i], "COLLATE"):
			key = "collation"
			i++
		case sqltext.Word(ts[i], "ENCRYPTION"):
			key = "encryption"
			i++
		default:
			return v, errors.New("unrecognized database option")
		}
		if i < len(ts) && ts[i].Text == "=" {
			i++
		}
		if i >= len(ts) {
			return v, errors.New("missing database option value")
		}
		if key == "charset" || key == "collation" {
			value, e := verifiedAccountPart(ts[i])
			if e != nil {
				return v, e
			}
			value = strings.ToLower(value)
			if key == "charset" && value == "utf8" {
				value = "utf8mb3"
			}
			v.options[key] = value
		} else {
			v.options[key] = verifiedTokens(ts[i : i+1])
		}
		i++
	}
	return v, nil
}
func parseVerifiedEvent(v verifiedObject, ts []sqltext.Token) (verifiedObject, error) {
	stop := func(i int) bool {
		return sqltext.Word(ts[i], "STARTS") || sqltext.Word(ts[i], "ENDS") || sqltext.Word(ts[i], "ENABLE") || sqltext.Word(ts[i], "DISABLE") || sqltext.Word(ts[i], "COMMENT") || sqltext.Word(ts[i], "DO") || sqltext.Word(ts[i], "ON") && i+1 < len(ts) && sqltext.Word(ts[i+1], "COMPLETION")
	}
	for i := 0; i < len(ts); {
		switch {
		case sqltext.Word(ts[i], "DO"):
			v.body = ts[i+1:]
			return v, nil
		case sqltext.Word(ts[i], "ON") && i+1 < len(ts) && sqltext.Word(ts[i+1], "SCHEDULE"):
			i += 2
			if i >= len(ts) {
				return v, errors.New("missing event schedule")
			}
			sched := strings.ToUpper(ts[i].Text)
			if sched != "AT" && sched != "EVERY" {
				return v, errors.New("unknown event schedule")
			}
			i++
			start := i
			for i < len(ts) && !stop(i) {
				i++
			}
			if i == start {
				return v, errors.New("empty schedule expression")
			}
			v.options["schedule"] = sched + ":" + verifiedTokens(ts[start:i])
		case sqltext.Word(ts[i], "STARTS") || sqltext.Word(ts[i], "ENDS"):
			key := strings.ToLower(ts[i].Text)
			i++
			start := i
			for i < len(ts) && !stop(i) {
				i++
			}
			if i == start {
				return v, errors.New("missing schedule boundary")
			}
			v.options[key] = verifiedTokens(ts[start:i])
		case sqltext.Word(ts[i], "ON") && i+1 < len(ts) && sqltext.Word(ts[i+1], "COMPLETION"):
			i += 2
			value := "PRESERVE"
			if i < len(ts) && sqltext.Word(ts[i], "NOT") {
				value = "NOT PRESERVE"
				i++
			}
			if i >= len(ts) || !sqltext.Word(ts[i], "PRESERVE") {
				return v, errors.New("unknown completion policy")
			}
			v.options["completion"] = value
			i++
		case sqltext.Word(ts[i], "ENABLE"):
			v.options["status"] = "ENABLE"
			i++
		case sqltext.Word(ts[i], "DISABLE"):
			v.options["status"] = "DISABLE"
			i++
			if i+1 < len(ts) && sqltext.Word(ts[i], "ON") && sqltext.Word(ts[i+1], "SLAVE") {
				v.options["status"] = "DISABLE ON SLAVE"
				i += 2
			}
		case sqltext.Word(ts[i], "COMMENT"):
			i++
			if i >= len(ts) || !ts[i].Quoted {
				return v, errors.New("bad event comment")
			}
			v.options["comment"] = verifiedTokens(ts[i : i+1])
			i++
		default:
			return v, errors.New("unknown event option")
		}
	}
	return v, nil
}

var verificationKeywords = func() map[string]bool {
	m := map[string]bool{}
	for _, k := range strings.Fields("SELECT FROM WHERE ORDER BY GROUP HAVING LIMIT OFFSET INSERT INTO VALUES UPDATE SET DELETE JOIN LEFT RIGHT INNER OUTER ON AS AND OR NOT NULL IS CREATE ALTER TABLE VIEW INDEX PRIMARY KEY FOREIGN REFERENCES UNIQUE DEFAULT CHECK CONSTRAINT BEGIN END WHEN THEN ELSE CASE UNION ALL DISTINCT ASC DESC EXPLAIN RETURNING RETURNS RETURN DETERMINISTIC LANGUAGE SQL SECURITY DEFINER INVOKER CONTAINS NO READS MODIFIES DATA COMMENT INT INTEGER TINYINT SMALLINT MEDIUMINT BIGINT DECIMAL NUMERIC FLOAT DOUBLE REAL BOOLEAN BOOL BIT CHAR VARCHAR BINARY VARBINARY TEXT TINYTEXT MEDIUMTEXT LONGTEXT BLOB TINYBLOB MEDIUMBLOB LONGBLOB DATE TIME DATETIME TIMESTAMP YEAR UNSIGNED ZEROFILL COLLATE CHARACTER CURRENT_TIMESTAMP CURRENT_DATE CURRENT_TIME IF EXISTS FOR EACH ROW CALL IN OUT INOUT TRUE FALSE COUNT SUM MIN MAX AVG CAST CONVERT EXISTS WITH ROLLUP OPTION CASCADED LOCAL INTERVAL DAY HOUR MINUTE SECOND YEAR MONTH WEEK QUARTER MICROSECOND DAY_HOUR DAY_MINUTE DAY_SECOND DAY_MICROSECOND HOUR_MINUTE HOUR_SECOND HOUR_MICROSECOND MINUTE_SECOND MINUTE_MICROSECOND SECOND_MICROSECOND YEAR_MONTH") {
		m[k] = true
	}
	return m
}()

func verifiedTokens(ts []sqltext.Token) string {
	var b strings.Builder
	for _, t := range ts {
		var text string
		switch {
		case t.Comment:
			text = "comment:" + t.Text
		case t.Quoted && strings.HasPrefix(t.Text, "`"):
			text = "word:" + unquoteObjectToken(t)
		case t.Quoted:
			text = "literal:" + t.Text
		case verificationKeywords[strings.ToUpper(t.Text)]:
			text = "keyword:" + strings.ToUpper(t.Text)
		default:
			text = "word:" + t.Text
		}
		fmt.Fprintf(&b, "%d:%s", len(text), text)
	}
	return b.String()
}

func readObjectVerificationContext(ctx context.Context, c *sql.Conn, p ObjectPlan, kind string) (ObjectVerificationContext, error) {
	v := ObjectVerificationContext{}
	if kind != "DATABASE" {
		if e := c.QueryRowContext(ctx, "SELECT @@SESSION.sql_mode, CURRENT_USER()").Scan(&v.SQLMode, &v.CurrentUser); e != nil {
			return v, e
		}
		if kind == "EVENT" {
			var created, altered, zone, sessionZone string
			if e := c.QueryRowContext(ctx, "SELECT CREATED,LAST_ALTERED,TIME_ZONE,@@SESSION.time_zone FROM information_schema.EVENTS WHERE EVENT_SCHEMA=? AND EVENT_NAME=?", p.Original.Database, p.Original.Name).Scan(&created, &altered, &zone, &sessionZone); e != nil {
				return v, e
			}
			if zone == sessionZone {
				stamp := created
				if p.Action == "alter" {
					stamp = altered
				}
				v.EventDefaultStart = verifiedTokens([]sqltext.Token{{Text: sqltext.Literal(stamp), Quoted: true}})
			}
		}
		return v, nil
	}
	want, e := parseVerifiedObject(p.SQL, kind, p.Original.ObjectIdentity)
	if e != nil {
		return v, e
	}
	expected := map[string]string{}
	if p.Action == "alter" {
		base, e := parseVerifiedObject(p.Original.SQL, kind, p.Original.ObjectIdentity)
		if e != nil {
			return v, e
		}
		expected = copyVerifiedOptions(base.options)
	}
	for k, value := range want.options {
		expected[k] = value
	}
	if _, setCharset := want.options["charset"]; setCharset {
		if _, setCollation := want.options["collation"]; !setCollation {
			delete(expected, "collation")
		}
	}
	if _, setCollation := want.options["collation"]; setCollation {
		if _, setCharset := want.options["charset"]; !setCharset {
			delete(expected, "charset")
		}
	}
	if expected["charset"] == "" && expected["collation"] == "" {
		var charset, collation string
		if e = c.QueryRowContext(ctx, "SELECT @@character_set_server,@@collation_server").Scan(&charset, &collation); e != nil {
			return v, e
		}
		expected["charset"], expected["collation"] = strings.ToLower(charset), strings.ToLower(collation)
	}
	if expected["charset"] == "" {
		var name string
		if e = c.QueryRowContext(ctx, "SELECT CHARACTER_SET_NAME FROM information_schema.COLLATIONS WHERE COLLATION_NAME=?", expected["collation"]).Scan(&name); e != nil {
			return v, e
		}
		expected["charset"] = strings.ToLower(name)
	}
	if expected["collation"] == "" {
		var name string
		if e = c.QueryRowContext(ctx, "SELECT DEFAULT_COLLATE_NAME FROM information_schema.CHARACTER_SETS WHERE CHARACTER_SET_NAME=?", expected["charset"]).Scan(&name); e != nil {
			return v, e
		}
		expected["collation"] = strings.ToLower(name)
	}
	if expected["encryption"] == "" {
		var enabled int
		if e = c.QueryRowContext(ctx, "SELECT @@default_table_encryption").Scan(&enabled); e != nil {
			return v, e
		}
		value := "'N'"
		if enabled != 0 {
			value = "'Y'"
		}
		expected["encryption"] = verifiedTokens([]sqltext.Token{{Text: value, Quoted: true}})
	}
	v.DatabaseCharset, v.DatabaseCollation, v.DatabaseEncryption = expected["charset"], expected["collation"], expected["encryption"]
	return v, nil
}
