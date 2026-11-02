package engines

import (
	"testing"

	"github.com/nyakang/nyadb/internal/db/schemaddl"
)

func TestPostgreSQL17DesignNativeRetainedPropertiesFixture(t *testing.T) {
	s := postgresDesignFixture(t)
	run(t, s, `CREATE SCHEMA nyadb_native_properties;
CREATE TABLE nyadb_native_properties.original(id smallint GENERATED ALWAYS AS IDENTITY (START WITH 100 CACHE 13), label text);
ALTER TABLE nyadb_native_properties.original ALTER COLUMN label SET STORAGE EXTERNAL;
ALTER TABLE nyadb_native_properties.original ALTER COLUMN label SET COMPRESSION pglz;
ALTER TABLE nyadb_native_properties.original ALTER COLUMN label SET STATISTICS 73;
INSERT INTO nyadb_native_properties.original(label) VALUES('preserved')`)
	base := pgDesignRead(t, s, "nyadb_native_properties", "original")
	draft := base.Schema.Table.Clone()
	pgNativeEditField(t, &draft, "label", func(f *schemaddl.PostgreSQLField) { f.Type = "character varying(100)" })
	base = pgNativeFixtureApply(t, s, base, draft, false)
	if properties := pgNativeField(t, &base.Schema.Table, "label").Postgres; properties.Storage != "e" || properties.Compression != "p" || properties.Statistics != 73 {
		t.Fatal("native TYPE reset retained column properties")
	}
	draft = base.Schema.Table.Clone()
	var lastBefore, lastAfter int64
	if err := s.db.QueryRow(`SELECT last_value FROM nyadb_native_properties.original_id_seq`).Scan(&lastBefore); err != nil {
		t.Fatal(err)
	}
	pgNativeEditField(t, &draft, "id", func(f *schemaddl.PostgreSQLField) { f.Type = "bigint" })
	base = pgNativeFixtureApply(t, s, base, draft, false)
	if err := s.db.QueryRow(`SELECT last_value FROM nyadb_native_properties.original_id_seq`).Scan(&lastAfter); err != nil || lastBefore != lastAfter {
		t.Fatal("identity type widening reset current sequence value", lastBefore, lastAfter, err)
	}
	var minimum, maximum, value, start, cache int64
	if err := s.db.QueryRow(`SELECT seqmin,seqmax,seqstart,seqcache FROM pg_sequence WHERE seqrelid='nyadb_native_properties.original_id_seq'::regclass`).Scan(&minimum, &maximum, &start, &cache); err != nil || minimum != 1 || maximum != 32767 || start != 100 || cache != 13 {
		t.Fatal("identity type widening changed requested sequence bounds", minimum, maximum, start, cache, err)
	}
	if err := s.db.QueryRow(`SELECT id FROM nyadb_native_properties.original`).Scan(&value); err != nil || value != 100 {
		t.Fatal("identity type widening changed existing values", value, err)
	}
}
