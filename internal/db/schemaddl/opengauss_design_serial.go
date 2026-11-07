package schemaddl

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Native ALTER ADD cannot expand serial declarations. Create the independent
// ordinary sequence explicitly and bind its ownership to the new numeric
// field. No existing sequence may be reused, advanced or reset by this plan.
func gaussDesignAddField(original Snapshot, col Clause, schema, table string) ([]string, error) {
	target := Quote(schema) + "." + Quote(table)
	f, err := ParseOpenGaussField(col.SQL)
	if err != nil {
		return nil, err
	}
	typ := ""
	switch strings.ToLower(f.Type) {
	case "bigserial", "serial8":
		typ = "bigint"
	case "serial", "serial4":
		typ = "integer"
	case "smallserial", "serial2":
		typ = "smallint"
	default:
		return []string{"ALTER TABLE " + target + " ADD COLUMN " + Quote(col.Name) + " " + col.SQL}, nil
	}
	if f.HasDefault || f.Generation != "" || f.Collation != "" {
		return nil, errors.New("native serial field cannot override its owned sequence default")
	}
	// Native ADD also forbids nextval defaults. Explicit backfill is an UPDATE,
	// so reject existing user triggers/rules and policies before creating the
	// sequence. The executor must additionally verify all native constraints,
	// generated expressions, access methods and casts under the original lock.
	for _, entry := range original.OpenGauss.Entries {
		switch entry.Kind {
		case "rule", "policy":
			return nil, errors.New("native serial backfill requires no user rules or row policies")
		case "trigger":
			var trigger struct {
				Internal bool `json:"tgisinternal"`
			}
			if json.Unmarshal(entry.Data, &trigger) != nil || !trigger.Internal {
				return nil, errors.New("native serial backfill requires no user triggers")
			}
		}
	}
	// Editors can reuse a new-field ID in a later draft. The captured revision
	// and field name keep that later sequence independent of earlier saves;
	// preview and execution of the same immutable request still agree.
	digest := sha256.Sum256([]byte(schema + "\x00" + table + "\x00" + original.Revision + "\x00" + col.ID + "\x00" + col.Name))
	name := fmt.Sprintf("nyadb_sequence_%x", digest[:16])
	sequence := Quote(schema) + "." + Quote(name)
	literal, err := postgresComment(sequence)
	if err != nil {
		return nil, err
	}
	f.Type, f.NotNull, f.HasDefault, f.Default = typ, false, false, ""
	body, err := f.SQL()
	if err != nil {
		return nil, err
	}
	return []string{
		"CREATE SEQUENCE " + sequence + " INCREMENT BY 1 MINVALUE 1 MAXVALUE 9223372036854775807 START WITH 1 CACHE 1 NO CYCLE",
		"ALTER TABLE " + target + " ADD COLUMN " + Quote(col.Name) + " " + body,
		"ALTER SEQUENCE " + sequence + " OWNED BY " + target + "." + Quote(col.Name),
		"ALTER TABLE " + target + " ALTER COLUMN " + Quote(col.Name) + " SET DEFAULT nextval(" + literal + "::regclass)",
		"UPDATE " + target + " SET " + Quote(col.Name) + "=DEFAULT",
		"ALTER TABLE " + target + " ALTER COLUMN " + Quote(col.Name) + " SET NOT NULL",
	}, nil
}
