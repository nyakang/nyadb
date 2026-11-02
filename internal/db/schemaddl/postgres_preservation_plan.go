package schemaddl

import (
	"errors"
	"strconv"
	"strings"
)

// Native ALTER TYPE preserves some dependent object properties internally,
// while an explicit DROP/CREATE does not. Restore only understood ORIGINAL
// properties for retained assets, after the requested declaration and metadata.
// The retained-catalog verifier still rejects unknown or unrestored state.
func pgRetainedObjectMetadata(original Snapshot, desired Table, target string) ([]string, error) {
	records, err := pgRenameRecords(original.Postgres)
	if err != nil {
		return nil, err
	}
	oldConstraints := map[string]Clause{}
	for _, c := range original.Table.Clauses {
		if c.Kind == "constraint" {
			oldConstraints[c.ID] = c
		}
	}
	var statements []string
	for _, c := range desired.Clauses {
		if _, retained := oldConstraints[c.ID]; c.Kind != "constraint" || !retained {
			continue
		}
		text, ok := strings.CutPrefix(c.ID, "constraint:")
		oid, err := strconv.ParseInt(text, 10, 64)
		if !ok || err != nil || oid <= 0 || strconv.FormatInt(oid, 10) != text {
			return nil, ErrConflict
		}
		record := records[pgRenameKey("constraint", oid, 0)]
		if record == nil {
			return nil, ErrConflict
		}
		comment, present := record["comment"]
		if !present || comment == "" || comment == nil {
			continue
		}
		value, ok := comment.(string)
		if !ok {
			return nil, errors.New("invalid original PostgreSQL constraint comment")
		}
		literal, err := postgresComment(value)
		if err != nil {
			return nil, err
		}
		statements = append(statements, "COMMENT ON CONSTRAINT "+Quote(c.Name)+" ON "+target+" IS "+literal)
	}
	keptIndexes := map[string]bool{}
	for _, d := range desired.Definitions {
		if d.Kind == "index" {
			keptIndexes[d.Name] = true
		}
	}
	for _, entry := range original.Postgres.Entries {
		if entry.Kind != "index" {
			continue
		}
		record := records[pgRenameKey(entry.Kind, entry.OID, entry.SubID)]
		name, ok := record["name"].(string)
		if !ok || !keptIndexes[name] || pgRenameNumber(record["relation_oid"]) != original.Postgres.RelationOID || pgRenameNumber(record["constraint_oid"]) != 0 {
			continue
		}
		if PostgreSQLIdentifier(name) != nil {
			return nil, ErrConflict
		}
		for _, field := range []string{"indisclustered", "indisreplident"} {
			value, exists := record[field]
			if !exists {
				continue
			}
			flag, ok := value.(bool)
			if !ok {
				return nil, errors.New("invalid original PostgreSQL index designation")
			}
			if !flag {
				continue
			}
			if field == "indisclustered" {
				statements = append(statements, "ALTER TABLE ONLY "+target+" CLUSTER ON "+Quote(name))
			} else if original.Table.Postgres.ReplicaIdentity == "i" && desired.Postgres.ReplicaIdentity == "i" {
				statements = append(statements, "ALTER TABLE ONLY "+target+" REPLICA IDENTITY USING INDEX "+Quote(name))
			}
		}
	}
	return statements, nil
}
