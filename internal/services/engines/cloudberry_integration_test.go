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

func cloudberryFixture(t *testing.T) (*Manager, *Session, connections.Profile) {
	t.Helper()
	if os.Getenv("NYADB_CLOUDBERRY_IT") != "1" {
		t.Skip("requires an owned disposable Apache Cloudberry fixture")
	}
	port, err := strconv.ParseUint(os.Getenv("NYADB_CLOUDBERRY_PORT"), 10, 16)
	if err != nil || port == 0 {
		t.Fatal("NYADB_CLOUDBERRY_PORT is required")
	}
	p, err := connections.Defaults(connections.Cloudberry)
	if err != nil {
		t.Fatal(err)
	}
	p.ID, p.Name, p.Host, p.Port = 731, "Cloudberry fixture", "127.0.0.1", uint16(port)
	p.Database, p.Username, p.TLSMode = "gpadmin", "gpadmin", "disable"
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

func TestCloudberryNativeMetadataAndDefinitionRoundTripFixture(t *testing.T) {
	_, s, _ := cloudberryFixture(t)
	suffix := strconv.FormatInt(time.Now().UnixNano(), 36)
	source, target := "nyadb_cb_source_"+suffix, "nyadb_cb_target_"+suffix
	ctx := context.Background()
	run(t, s, "CREATE SCHEMA "+quote(source)+"; CREATE SCHEMA "+quote(target))
	t.Cleanup(func() { run(t, s, "DROP SCHEMA "+quote(target)+" CASCADE; DROP SCHEMA "+quote(source)+" CASCADE") })
	qs := quote(source) + "."
	run(t, s, "CREATE TABLE "+qs+`hash_events (tenant_id integer PRIMARY KEY, payload text DEFAULT 'original', amount numeric(40,20), bin bytea) DISTRIBUTED BY (tenant_id);
CREATE INDEX hash_payload ON `+qs+`hash_events(payload);
COMMENT ON TABLE `+qs+`hash_events IS 'tenant''s table'; COMMENT ON COLUMN `+qs+`hash_events.payload IS 'kept column';
CREATE TABLE `+qs+`random_events (id integer, payload text) DISTRIBUTED RANDOMLY;
CREATE TABLE `+qs+`replicated_dimensions (id integer, name text) DISTRIBUTED REPLICATED;
CREATE TABLE `+qs+`column_metrics (metric text, value numeric(18,4)) USING ao_column WITH (compresstype=zstd,compresslevel=3) DISTRIBUTED BY (metric);
CREATE TABLE `+qs+`partitioned_events (event_date date, tenant_id integer) PARTITION BY RANGE (event_date) DISTRIBUTED BY (tenant_id);
CREATE TABLE `+qs+`partition_leaf PARTITION OF `+qs+`partitioned_events FOR VALUES FROM ('2026-01-01') TO ('2027-01-01');
CREATE TABLE `+qs+`identity_events (id bigint GENERATED ALWAYS AS IDENTITY (START WITH 9007199254740993 INCREMENT BY 3 CACHE 11),payload text) DISTRIBUTED BY (id);
CREATE TABLE `+qs+`serial_events (id bigserial,payload text) DISTRIBUTED BY (id);
CREATE TABLE `+qs+`secure_events (id integer,payload text) DISTRIBUTED BY (id);
ALTER TABLE `+qs+`secure_events ENABLE ROW LEVEL SECURITY; ALTER TABLE `+qs+`secure_events FORCE ROW LEVEL SECURITY;
CREATE POLICY positive ON `+qs+`secure_events FOR ALL TO PUBLIC USING (id>0) WITH CHECK (id>0);
CREATE READABLE EXTERNAL TABLE `+qs+`external_events (id integer, payload text) LOCATION ('file://cdw/tmp/nyadb-cloudberry.csv') FORMAT 'CSV' (DELIMITER ',');
CREATE TABLE `+qs+`"quoted distribution" ("tenant,
""key" integer, payload text) DISTRIBUTED BY ("tenant,
""key");`)
	objects, err := s.Browse(ctx, s.Namespace(source))
	if err != nil {
		t.Fatal("native browse", err)
	}
	byName := map[string]contract.DBObject{}
	for _, object := range objects {
		if object.Ref.ProfileID != s.id || object.Ref.Catalog != s.Catalog() || object.Ref.Schema != source || object.Engine != "Apache Cloudberry" {
			t.Fatal("browse lost original qualified product identity")
		}
		byName[object.Name] = object
	}
	if byName["external_events"].Kind != "外部表" || byName["external_events"].Ref.Kind != "table" {
		t.Fatal("external table classification")
	}
	cases := []struct{ name, expected string }{
		{"hash_events", `DISTRIBUTED BY ("tenant_id")`},
		{"random_events", "DISTRIBUTED RANDOMLY"},
		{"replicated_dimensions", "DISTRIBUTED REPLICATED"},
		{"column_metrics", `USING "ao_column"`},
		{"partitioned_events", "PARTITION BY RANGE (event_date)"},
		{"partition_leaf", "ATTACH PARTITION"},
		{"identity_events", "START WITH 9007199254740993 INCREMENT BY 3"},
		{"serial_events", "OWNED BY"},
		{"secure_events", "FORCE ROW LEVEL SECURITY"},
		{"external_events", "CREATE FOREIGN TABLE"},
		{"quoted distribution", "DISTRIBUTED BY (\"tenant,\n\"\"key\")"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			o := s.Namespace(source).Object("table", test.name)
			info, err := s.TableMetadata(ctx, o)
			if err != nil {
				t.Fatal("original table metadata", err)
			}
			if !strings.Contains(info.Definition, test.expected) {
				t.Fatalf("missing native semantics %q: %s", test.expected, info.Definition)
			}
			if test.name == "external_events" && (info.Editable || info.ExternalServer != "gp_exttable_server" || !strings.Contains(info.Definition, `"location_uris" 'file://cdw/tmp/nyadb-cloudberry.csv'`)) {
				t.Fatal("external options were dropped", info.Definition)
			}
			if test.name == "hash_events" && (!info.Editable || len(info.Indexes) != 2 || info.Comment != "tenant's table" || info.Columns[1].Comment != "kept column") {
				t.Fatal("ordinary metadata changed")
			}
			// Native pg_get_* functions may omit quotes on simple identifiers.
			// The nonce schema name occurs only as fixture identity in this DDL.
			ddl := strings.ReplaceAll(info.Definition, source, target)
			run(t, s, ddl)
			copy, err := s.TableMetadata(ctx, s.Namespace(target).Object("table", test.name))
			if err != nil || copy.Distribution != info.Distribution || copy.Engine != info.Engine || !strings.Contains(copy.Definition, test.expected) {
				t.Fatal("round trip changed product metadata", err)
			}
			if test.name == "identity_events" && !strings.Contains(copy.Definition, "CACHE 11") {
				t.Fatal("identity sequence settings lost")
			}
		})
	}
	wrong := s.Namespace(source).Object("table", "hash_events")
	wrong.ProfileID++
	if _, err := s.TableMetadata(ctx, wrong); err == nil {
		t.Fatal("cross-profile table metadata accepted")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := s.TableMetadata(cancelled, s.Namespace(source).Object("table", "hash_events")); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled original metadata accepted", err)
	}
}

func TestCloudberryDataTransactionsPlansAndOriginalLeasesFixture(t *testing.T) {
	m, s, profile := cloudberryFixture(t)
	schema := "nyadb_cb_data_" + strconv.FormatInt(time.Now().UnixNano(), 36)
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
	run(t, s, "CREATE TABLE "+qualified(ref)+` (id integer PRIMARY KEY,note text,amount numeric(40,20),payload bytea) DISTRIBUTED BY (id); INSERT INTO `+qualified(ref)+` VALUES (1,'old',9007199254740993.12345678901234567890,decode('`+hex.EncodeToString(payload)+`','hex')),(2,NULL,NULL,NULL)`)
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
	sql, err := queryplan.Build("cloudberry", "SELECT id FROM "+qualified(ref)+" WHERE id=1")
	if err != nil {
		t.Fatal(err)
	}
	planResult, err := lease.Execute(ctx, sql)
	if err != nil {
		t.Fatal("native EXPLAIN", err)
	}
	if _, err = queryplan.Parse("cloudberry", planResult); err != nil {
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
