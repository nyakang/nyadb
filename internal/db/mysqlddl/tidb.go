package mysqlddl

import (
	"errors"
	"strings"

	"github.com/nyakang/nyadb/internal/db/sqltext"
)

// TiDB uses executable comments for clustered keys, AUTO_RANDOM and placement
// attributes. Ordinary SQL comment canonicalization cannot erase their meaning.
func tidbDirectives(raw string) string {
	tokens, err := sqltext.Tokens(raw)
	if err != nil {
		return "invalid"
	}
	var parts []string
	for _, token := range tokens {
		if token.Comment && strings.HasPrefix(token.Text, "/*T!") {
			parts = append(parts, Canonical(strings.TrimSuffix(strings.TrimPrefix(token.Text, "/*T!"), "*/")))
		}
	}
	return strings.Join(parts, "\x00")
}

func expandTiDBVersionComments(raw string) string {
	tokens, err := sqltext.Tokens(raw)
	if err != nil {
		return raw
	}
	for i := len(tokens) - 1; i >= 0; i-- {
		token := tokens[i]
		if !token.Comment || !strings.HasPrefix(token.Text, "/*!") {
			continue
		}
		body := strings.TrimSuffix(strings.TrimPrefix(token.Text, "/*!"), "*/")
		at := 0
		for at < len(body) && body[at] >= '0' && body[at] <= '9' {
			at++
		}
		raw = raw[:token.Start] + " " + body[at:] + " " + raw[token.End:]
	}
	return raw
}

func tableDesignCanonical(engine, raw string) string {
	if engine == "tidb" {
		raw = expandTiDBVersionComments(raw)
	}
	return Canonical(raw)
}

func validateTiDBDraft(original Snapshot, draft Draft) error {
	if len(draft.Triggers) > 0 || len(original.Triggers) > 0 {
		return errors.New("TiDB does not support triggers")
	}
	engine := Option(draft.Table.Options, "ENGINE")
	if engine != "" && !strings.EqualFold(engine, "InnoDB") {
		return errors.New("TiDB does not implement MySQL storage engine selection")
	}
	old := map[string]Clause{}
	if !draft.New {
		base, err := Parse(original.Database, original.Table, original.DDL)
		if err != nil {
			return err
		}
		for _, clause := range base.Clauses {
			old[clause.ID] = clause
		}
		if tidbDirectives(base.Options) != tidbDirectives(draft.Table.Options) {
			return errors.New("TiDB table-specific attributes must remain intact")
		}
	}
	for _, clause := range draft.Table.Clauses {
		previous, exists := old[clause.ID]
		if exists && tidbDirectives(previous.SQL) != tidbDirectives(clause.SQL) {
			return errors.New("TiDB clustered/AUTO_RANDOM attributes must remain intact")
		}
		delete(old, clause.ID)
		if exists && Canonical(previous.SQL) == Canonical(clause.SQL) {
			continue
		}
		tokens, err := significant(clause.SQL)
		if err != nil {
			return err
		}
		for _, token := range tokens {
			if !draft.New && clause.Kind == Column && sqltext.Word(token, "CHECK") {
				return errors.New("TiDB inline CHECK cannot be added with ALTER COLUMN; use a table CHECK constraint")
			}
			if (clause.Kind == Index || clause.Kind == Primary) && (sqltext.Word(token, "FULLTEXT") || sqltext.Word(token, "SPATIAL") || sqltext.Word(token, "HASH") || sqltext.Word(token, "RTREE") || sqltext.Word(token, "DESC")) {
				return errors.New("TiDB does not implement the requested MySQL index semantics")
			}
			if clause.Kind == Column && (sqltext.Word(token, "GEOMETRY") || sqltext.Word(token, "POINT") || sqltext.Word(token, "LINESTRING") || sqltext.Word(token, "POLYGON") || sqltext.Word(token, "MULTIPOINT") || sqltext.Word(token, "MULTILINESTRING") || sqltext.Word(token, "MULTIPOLYGON") || sqltext.Word(token, "GEOMETRYCOLLECTION")) {
				return errors.New("TiDB does not support spatial field types")
			}
		}
	}
	for _, clause := range old {
		if tidbDirectives(clause.SQL) != "" {
			return errors.New("TiDB clustered/AUTO_RANDOM field or key cannot be removed through the designer")
		}
	}
	return nil
}

func verifyTiDBDirectives(desired Draft, actual Table) error {
	got := map[string]Clause{}
	for _, clause := range actual.Clauses {
		got[string(clause.Kind)+":"+clause.Name] = clause
	}
	for _, clause := range desired.Table.Clauses {
		other := got[string(clause.Kind)+":"+clause.Name]
		want, value := tidbDirectives(clause.SQL), tidbDirectives(other.SQL)
		if want == value {
			continue
		}
		// A new ordinary primary key may acquire TiDB's configured clustered
		// default. Existing keys and explicitly requested attributes must match.
		if desired.New && want == "" && clause.Kind == Primary && (value == Canonical("[clustered_index] CLUSTERED") || value == Canonical("[clustered_index] NONCLUSTERED")) {
			continue
		}
		return errors.New("reloaded TiDB field/key attributes differ from the requested definition")
	}
	if tidbDirectives(desired.Table.Options) != tidbDirectives(actual.Options) {
		return errors.New("reloaded TiDB table attributes differ from the requested definition")
	}
	return nil
}
