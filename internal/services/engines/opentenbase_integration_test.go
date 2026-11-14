package engines

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/queryplan"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func openTenBaseFixture(t *testing.T) (*Manager, *Session, connections.Profile) {
	t.Helper()
	if os.Getenv("NYADB_OPENTENBASE_IT") != "1" {
		t.Skip("requires an owned disposable OpenTenBase fixture")
	}
	port, err := strconv.ParseUint(os.Getenv("NYADB_OPENTENBASE_PORT"), 10, 16)
	if err != nil || port == 0 {
		t.Fatal("NYADB_OPENTENBASE_PORT is required")
	}
	p, err := connections.Defaults(connections.OpenTenBase)
	if err != nil {
		t.Fatal(err)
	}
	p.ID, p.Name, p.Host, p.Port = 731, "OpenTenBase fixture", "127.0.0.1", uint16(port)
	p.Database, p.Username, p.TLSMode = "postgres", "opentenbase", "disable"
	m := NewManager()
	t.Cleanup(func() { _ = m.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := m.Connect(ctx, p, ""); err != nil {
		t.Fatal(err)
	}
	s, err := m.Session(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	return m, s, p
}

func TestOpenTenBaseNativeMetadataAndDefinitionRoundTripFixture(t *testing.T) {
	_, s, _ := openTenBaseFixture(t)
	suffix := strconv.FormatInt(time.Now().UnixNano(), 36)
	source, target := "nyadb_otb_source_"+suffix, "nyadb_otb_target_"+suffix
	run(t, s, "CREATE SCHEMA "+quote(source)+"; CREATE SCHEMA "+quote(target))
	t.Cleanup(func() { run(t, s, "DROP SCHEMA "+quote(target)+" CASCADE; DROP SCHEMA "+quote(source)+" CASCADE") })
	qs := quote(source) + "."
	run(t, s, "CREATE TABLE "+qs+`shard_events (tenant_id integer PRIMARY KEY, payload text DEFAULT 'original', amount numeric(40,20), bin bytea) DISTRIBUTE BY SHARD (tenant_id);
CREATE INDEX shard_payload ON `+qs+`shard_events(payload);
COMMENT ON TABLE `+qs+`shard_events IS 'tenant''s table'; COMMENT ON COLUMN `+qs+`shard_events.payload IS 'kept column';
CREATE TABLE `+qs+`hash_events (id integer, payload text) DISTRIBUTE BY HASH (id);
CREATE TABLE `+qs+`modulo_events (id integer, payload text) DISTRIBUTE BY MODULO (id);
CREATE TABLE `+qs+`random_events (id integer, payload text) DISTRIBUTE BY ROUNDROBIN;
CREATE TABLE `+qs+`replicated_dimensions (id integer, name text) DISTRIBUTE BY REPLICATION;
CREATE TABLE `+qs+`partitioned_events (event_date date, tenant_id integer) PARTITION BY RANGE (event_date) DISTRIBUTE BY SHARD (tenant_id);
CREATE TABLE `+qs+`partition_leaf PARTITION OF `+qs+`partitioned_events FOR VALUES FROM ('2026-01-01') TO ('2027-01-01');
CREATE TABLE `+qs+`serial_events (id bigserial PRIMARY KEY,payload text) DISTRIBUTE BY SHARD (id);
CREATE TABLE `+qs+`secure_events (id integer,payload text) DISTRIBUTE BY SHARD (id);
ALTER TABLE `+qs+`secure_events ENABLE ROW LEVEL SECURITY; ALTER TABLE `+qs+`secure_events FORCE ROW LEVEL SECURITY;
CREATE POLICY positive ON `+qs+`secure_events FOR ALL TO PUBLIC USING (id>0) WITH CHECK (id>0);
CREATE TABLE `+qs+`"quoted distribution" ("tenant,
""key" integer, payload text) DISTRIBUTE BY SHARD ("tenant,
""key");`)
	ctx := context.Background()
	objects, err := s.Browse(ctx, s.Namespace(source))
	if err != nil {
		t.Fatal("native browse", err)
	}
	for _, object := range objects {
		if object.Ref.ProfileID != s.id || object.Ref.Catalog != s.Catalog() || object.Ref.Schema != source || object.Engine != "OpenTenBase" {
			t.Fatal("native browse changed original identity")
		}
	}
	cases := []struct{ name, clause string }{
		{"shard_events", `DISTRIBUTE BY SHARD ("tenant_id")`},
		{"hash_events", `DISTRIBUTE BY HASH ("id")`},
		{"modulo_events", `DISTRIBUTE BY MODULO ("id")`},
		{"random_events", "DISTRIBUTE BY ROUNDROBIN"},
		{"replicated_dimensions", "DISTRIBUTE BY REPLICATION"},
		{"partitioned_events", `DISTRIBUTE BY SHARD ("tenant_id")`},
		{"serial_events", `DISTRIBUTE BY SHARD ("id")`},
		{"secure_events", `DISTRIBUTE BY SHARD ("id")`},
		{"quoted distribution", "DISTRIBUTE BY SHARD (\"tenant,\n\"\"key\")"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			info, err := s.TableMetadata(ctx, s.Namespace(source).Object("table", test.name))
			if err != nil || info.Distribution != test.clause || info.Definition == "" {
				t.Fatal("native distribution/definition", err)
			}
			if test.name == "shard_events" && (!info.Editable || len(info.Indexes) != 2 || info.Comment != "tenant's table" || info.Columns[1].Comment != "kept column") {
				t.Fatal("native ordinary metadata changed")
			}
			// Round-trip the full native definition including original placement
			// and partition tree. Only this nonce schema identity is replaced.
			ddl := strings.ReplaceAll(info.Definition, source, target)
			lease, err := s.Reserve(ctx)
			if err != nil {
				t.Fatal(err)
			}
			_, err = lease.Execute(ctx, ddl)
			_ = lease.Close()
			if err != nil {
				t.Fatalf("native definition replay: %v\n%s", err, ddl)
			}
			copy, err := s.TableMetadata(ctx, s.Namespace(target).Object("table", test.name))
			if err != nil || copy.Distribution != info.Distribution || copy.Engine != info.Engine {
				t.Fatal("native round trip changed distribution or storage", err)
			}
			if len(copy.Columns) != len(info.Columns) {
				t.Fatal("native round trip changed field count")
			}
			for i, original := range info.Columns {
				column := copy.Columns[i]
				if column.Name != original.Name || column.Type != original.Type || column.Nullable != original.Nullable || column.Generated != original.Generated || (column.Default == nil) != (original.Default == nil) {
					t.Fatal("native round trip changed field semantics", original.Name)
				}
				if original.Default != nil && *column.Default != strings.ReplaceAll(*original.Default, source, target) {
					t.Fatal("native round trip changed default expression", original.Name)
				}
			}
			if test.name == "shard_events" && (copy.Comment != info.Comment || copy.Columns[1].Comment != info.Columns[1].Comment || len(copy.Indexes) != len(info.Indexes)) {
				t.Fatal("native round trip changed comments or indexes")
			}
			if test.name == "secure_events" && !strings.Contains(copy.Definition, "FORCE ROW LEVEL SECURITY") {
				t.Fatal("native round trip lost row security")
			}
			if test.name == "serial_events" {
				for _, namespace := range []string{source, target} {
					result := run(t, s, "INSERT INTO "+quote(namespace)+`.serial_events(payload) VALUES ('sequence retained') RETURNING id`)
					if len(result.Rows) != 1 || result.Rows[0][0] != "1" {
						t.Fatal("native round trip lost serial allocation")
					}
				}
			}
		})
	}
	for _, schema := range []string{source, target} {
		leaf, err := s.TableMetadata(ctx, s.Namespace(schema).Object("table", "partition_leaf"))
		if err != nil || !strings.Contains(leaf.Definition, "PARTITION OF") {
			t.Fatal("native partition identity lost", err)
		}
	}
	wrong := s.Namespace(source).Object("table", "shard_events")
	wrong.ProfileID++
	if _, err := s.TableMetadata(ctx, wrong); err == nil {
		t.Fatal("cross-profile native metadata accepted")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := s.TableMetadata(cancelled, s.Namespace(source).Object("table", "shard_events")); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled original metadata accepted", err)
	}
}

func TestOpenTenBaseDataTransactionsPlansAndOriginalLeasesFixture(t *testing.T) {
	m, s, profile := openTenBaseFixture(t)
	schema := "nyadb_otb_data_" + strconv.FormatInt(time.Now().UnixNano(), 36)
	run(t, s, "CREATE SCHEMA "+quote(schema))
	// Cleanup uses the replacement handle if the original session is revoked.
	t.Cleanup(func() {
		current, err := m.Session(profile.ID)
		if err == nil {
			run(t, current, "DROP SCHEMA "+quote(schema)+" CASCADE")
		}
	})
	ref := s.Namespace(schema).Object("table", "records")
	payload := bytes.Repeat([]byte{0, 255}, 700)
	run(t, s, "CREATE TABLE "+qualified(ref)+` (id integer PRIMARY KEY,note text,amount numeric(40,20),payload bytea) DISTRIBUTE BY SHARD (id); INSERT INTO `+qualified(ref)+` VALUES (1,'old',9007199254740993.12345678901234567890,decode('`+hex.EncodeToString(payload)+`','hex')),(2,NULL,NULL,NULL)`)
	ctx := context.Background()
	data := s.DataRuntime()
	page, err := data.AdvancedLoader(ctx, s.id, schema, "records", contract.DataOptions{Limit: 1, UseKeyset: true})
	if err != nil {
		t.Fatal("typed grid page", err)
	}
	if !page.KeysetAvailable || page.NextCursor == nil || page.Rows[0][2].Text != "9007199254740993.12345678901234567890" || page.Rows[0][2].Value.Kind != contract.Decimal || !page.Rows[0][3].Truncated || !bytes.Equal(page.Rows[0][3].Value.Bytes, payload[:1024]) {
		t.Fatal("page lost typed values or bounded preview")
	}
	next, err := data.AdvancedLoader(ctx, s.id, schema, "records", contract.DataOptions{Limit: 1, UseKeyset: true, After: page.NextCursor})
	if err != nil || len(next.Rows) != 1 || next.Rows[0][0].Text != "2" || !next.Rows[0][1].Null {
		t.Fatal("original cursor or NULL", err)
	}
	complete, err := data.RowReader(ctx, s.id, schema, "records", *page.RowRefs[0])
	if err != nil || complete.Snapshot == nil || !bytes.Equal(complete.Values["payload"].Bytes, payload) {
		t.Fatal("complete original editing snapshot", err)
	}
	chunk, err := data.Chunk(ctx, s.id, schema, "records", "payload", *page.RowRefs[0], 1023, 5)
	if err != nil || !chunk.Binary || chunk.Text != "0x"+hex.EncodeToString(payload[1023:1028]) || chunk.NextOffset != 1028 {
		t.Fatal("explicit bounded binary chunk", err)
	}
	exported := 0
	err = data.Exporter(ctx, s.id, schema, "records", contract.DataOptions{}, []string{"id", "amount", "payload"}, func(values []contract.DataValue) error {
		if exported == 0 && (values[1].Kind != contract.Decimal || values[1].Text != "9007199254740993.12345678901234567890" || !values[2].Binary || !bytes.Equal(values[2].Bytes, payload)) {
			t.Fatal("streaming export changed exact values")
		}
		if exported == 1 && (!values[1].Null || !values[2].Null) {
			t.Fatal("streaming export changed NULL")
		}
		exported++
		return nil
	})
	if err != nil || exported != 2 {
		t.Fatal("complete export", err)
	}
	edit := contract.DataEdit{Kind: "update", Original: complete.Snapshot, Values: map[string]contract.DataValue{"note": {Text: "saved"}}}
	result, err := data.Saver(ctx, s.id, schema, "records", []contract.DataEdit{edit})
	if err != nil || !result.CommitConfirmed || result.Applied != 1 {
		t.Fatal("original row save", err)
	}
	if _, err := data.Saver(ctx, s.id, schema, "records", []contract.DataEdit{edit}); !errors.Is(err, ErrConflict) {
		t.Fatal("stale fingerprint accepted", err)
	}
	plain, err := s.Page(ctx, contract.PageRequest{Object: ref, Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	tx, err := s.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Close()
	_, err = tx.Apply(ctx, ref, []contract.Edit{{Kind: "update", Identity: plain.Identities[0], Original: plain.Rows[0], Values: map[string]contract.Cell{"note": {Kind: contract.Text, Text: "rolled back"}}}})
	if err != nil {
		t.Fatal("explicit transaction edit", err)
	}
	outside, err := s.Page(ctx, contract.PageRequest{Object: ref, Limit: 1})
	if err != nil || outside.Rows[0][1].Text != "saved" {
		t.Fatal("uncommitted write escaped the original transaction", err)
	}
	if err = tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	after, err := s.Page(ctx, contract.PageRequest{Object: ref, Limit: 1})
	if err != nil || after.Rows[0][1].Text != "saved" {
		t.Fatal("rollback changed persisted row", err)
	}
	committing, err := s.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer committing.Close()
	if _, err = committing.Apply(ctx, ref, []contract.Edit{{Kind: "update", Identity: after.Identities[0], Original: after.Rows[0], Values: map[string]contract.Cell{"note": {Kind: contract.Text, Text: "committed"}}}}); err != nil {
		t.Fatal(err)
	}
	receipt, err := committing.Commit(ctx)
	if err != nil || !receipt.CommitConfirmed {
		t.Fatal("explicit commit receipt", err)
	}
	committed, err := s.Page(ctx, contract.PageRequest{Object: ref, Limit: 1})
	if err != nil || committed.Rows[0][1].Text != "committed" {
		t.Fatal("confirmed commit did not persist", err)
	}
	lease, err := s.Reserve(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	sql, err := queryplan.Build("opentenbase", "SELECT id FROM "+qualified(ref)+" WHERE id=1")
	if err != nil {
		t.Fatal(err)
	}
	planResult, err := lease.Execute(ctx, sql)
	if err != nil {
		t.Fatal("native EXPLAIN", err)
	}
	if _, err = queryplan.Parse("opentenbase", planResult); err != nil {
		t.Fatal("native plan parse", err)
	}
	if err = m.Connect(ctx, profile, ""); err != nil {
		t.Fatal(err)
	}
	replacement, _ := m.Session(profile.ID)
	if replacement.Generation() <= s.Generation() {
		t.Fatal("session generation did not advance")
	}
	if _, err = lease.Execute(ctx, "SELECT 1"); !errors.Is(err, ErrRevoked) {
		t.Fatal("original SQL lease survived replacement", err)
	}
	if _, err = data.AdvancedLoader(ctx, s.id, schema, "records", contract.DataOptions{Limit: 1}); !errors.Is(err, ErrRevoked) {
		t.Fatal("original data runtime survived replacement", err)
	}
}

func TestOpenTenBaseNativeEditKeyEligibilityFixture(t *testing.T) {
	_, s, _ := openTenBaseFixture(t)
	schema := "nyadb_otb_keys_" + strconv.FormatInt(time.Now().UnixNano(), 36)
	run(t, s, "CREATE SCHEMA "+quote(schema))
	t.Cleanup(func() { run(t, s, "DROP SCHEMA "+quote(schema)+" CASCADE") })
	qs := quote(schema) + "."
	run(t, s, "CREATE TABLE "+qs+`reliable (tenant integer NOT NULL,id integer NOT NULL,payload text,PRIMARY KEY(tenant,id)) DISTRIBUTE BY SHARD (tenant);
CREATE TABLE `+qs+`nullable_unique (id integer UNIQUE,payload text) DISTRIBUTE BY SHARD (id);
CREATE TABLE `+qs+`partial_unique (id integer NOT NULL,payload text) DISTRIBUTE BY SHARD (id);
CREATE UNIQUE INDEX partial_key ON `+qs+`partial_unique(id) WHERE id>0;
CREATE TABLE `+qs+`deferred_unique (id integer PRIMARY KEY DEFERRABLE INITIALLY DEFERRED,payload text) DISTRIBUTE BY SHARD (id);
CREATE TABLE `+qs+`custom_operator (id text NOT NULL,payload text) DISTRIBUTE BY SHARD (id);
CREATE UNIQUE INDEX pattern_key ON `+qs+`custom_operator(id text_pattern_ops);`)
	for _, test := range []struct {
		name     string
		editable bool
	}{
		{"reliable", true},
		{"nullable_unique", false},
		{"partial_unique", false},
		{"deferred_unique", false},
		{"custom_operator", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			ref := s.Namespace(schema).Object("table", test.name)
			structure, err := s.Structure(context.Background(), ref)
			if err != nil {
				t.Fatal(err)
			}
			if structure.Editable != test.editable || !test.editable && len(structure.Key) != 0 {
				t.Fatalf("native key eligibility changed: editable=%v key=%v", structure.Editable, structure.Key)
			}
			if test.editable && (len(structure.Key) != 2 || structure.Key[0] != "tenant" || structure.Key[1] != "id") {
				t.Fatal("native composite key lost member order", structure.Key)
			}
		})
	}
}
