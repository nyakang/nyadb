package trino

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func insertNativeSession(t *testing.T, managed bool) *Session {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	if managed {
		p, secret, provider := managedNativeProvider(t)
		raw, err := provider.Open(ctx, p, secret)
		if err != nil {
			t.Fatal(err)
		}
		s := raw.(*Session)
		t.Cleanup(func() { s.Close() })
		return s
	}
	cfg, secret := nativeConfig(t)
	p, _ := connections.Defaults(connections.Trino)
	u, _ := url.Parse(cfg.Endpoint)
	port, _ := strconv.Atoi(u.Port())
	p.ID, p.Host, p.Port, p.Username, p.Database = 399, u.Hostname(), uint16(port), cfg.Username, "memory"
	p.Name = "native insert fixture"
	p.TrinoOptions.DefaultSchema = "default"
	p.TrinoOptions.CAFile = cfg.CAFile
	if u.Scheme == "http" {
		p.TLSMode = "disable"
	}
	raw, err := OpenSession(ctx, p, secret)
	if err != nil {
		t.Fatal(err)
	}
	s := raw.(*Session)
	t.Cleanup(func() { s.Close() })
	return s
}
func TestTrinoNativeDocumentInsertPrecisionOmissionAndReceipts(t *testing.T) {
	verifyDocumentInsert(t, false)
}
func TestTrinoManagedNativeDocumentInsertPrecisionOmissionAndReceipts(t *testing.T) {
	verifyDocumentInsert(t, true)
}
func verifyDocumentInsert(t *testing.T, managed bool) {
	s := insertNativeSession(t, managed)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	if _, err := s.Namespaces(ctx); err != nil {
		t.Fatal(err)
	}
	query, err := s.ReserveQuery(3939)
	if err != nil {
		t.Fatal(err)
	}
	defer query.Release()
	run := func(sql string) contract.SQLResult {
		t.Helper()
		r, err := query.Execute(ctx, "", sql)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	name := "owned_insert_" + strings.ToLower(rand.Text())
	o := contract.NamespaceRef{ProfileID: s.ProfileID(), Catalog: "memory", Schema: "default"}.Object("TABLE", name)
	db := namespaceLabel(o.Catalog, o.Schema)
	run("CREATE TABLE " + qualified(o) + "(n BIGINT,d DECIMAL(38,20),b VARBINARY,a ARRAY(BIGINT),m MAP(VARCHAR,DECIMAL(38,20)),r ROW(n BIGINT,s VARCHAR),t TIME(12),ts TIMESTAMP(12))")
	defer run("DROP TABLE " + qualified(o))
	lease, err := s.ReserveDataDocument(o)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	rt := lease.Runtime()
	if !rt.Capabilities.EditableTables || rt.Transactions.Begin != nil || rt.RowReader != nil || !rt.SequentialWrites || len(rt.EditKinds) != 1 || rt.EditKinds[0] != "insert" {
		t.Fatal("insert capabilities broadened")
	}
	values := map[string]contract.DataValue{
		"n": {Kind: contract.Integer, Text: "9007199254740993"}, "d": {Kind: contract.Decimal, Text: "123456789012345678.12345678901234567890"}, "b": {Kind: contract.Binary, Bytes: []byte{0, 255}}, "a": {Kind: contract.JSON, Text: `[9007199254740993,null]`}, "m": {Kind: contract.JSON, Text: `{"中文":"123456789012345678.12345678901234567890"}`}, "r": {Kind: contract.JSON, Text: `[9007199254740993,"中文'quote"]`}, "t": {Kind: contract.DateTime, Text: "01:02:03.123456789123"}, "ts": {Kind: contract.DateTime, Text: "2026-11-16 01:02:03.123456789123"},
	}
	plan, err := rt.PreviewEdits(ctx, s.ProfileID(), db, name, []contract.DataEdit{{Kind: "insert", Values: values}})
	if err != nil {
		t.Fatal(err)
	}
	values["n"] = contract.DataValue{Kind: contract.Integer, Text: "2"}
	values["b"].Bytes[0] = 99
	snap := plan.Snapshot()
	if snap.Edits[0].Values["n"].Text != "9007199254740993" || snap.Edits[0].Values["b"].Bytes[0] != 0 {
		t.Fatal("caller changed original plan")
	}
	snap.Edits[0].Values["b"].Bytes[0] = 77
	other, err := s.ReserveDataDocument(o)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if _, err = other.Runtime().SubmitEdits(ctx, s.ProfileID(), db, name, plan); !errors.Is(err, connections.ErrInvalid) {
		t.Fatal("foreign plan authorized", err)
	}
	result, err := rt.SubmitEdits(ctx, s.ProfileID(), db, name, plan)
	if err != nil || !contract.DataWriteComplete(result, 1) || result.CommitConfirmed || result.Applied != 1 || !result.RefreshRequired {
		t.Fatal("native INSERT receipt incomplete", err, result)
	}
	if _, err = rt.SubmitEdits(ctx, s.ProfileID(), db, name, plan); err == nil {
		t.Fatal("original plan replayed")
	}
	read := run("SELECT CAST(n AS VARCHAR),CAST(d AS VARCHAR),to_hex(b),json_format(CAST(a AS JSON)),json_format(CAST(m AS JSON)),json_format(CAST(r AS JSON)),CAST(t AS VARCHAR),CAST(ts AS VARCHAR) FROM " + qualified(o))
	want := []string{"9007199254740993", "123456789012345678.12345678901234567890", "00FF", `[9007199254740993,null]`, `{"中文":123456789012345678.12345678901234567890}`, `{"n":9007199254740993,"s":"中文'quote"}`, "01:02:03.123456789123", "2026-11-16 01:02:03.123456789123"}
	if len(read.Rows) != 1 || len(read.Rows[0]) != len(want) {
		t.Fatal("complete native projection missing")
	}
	for i, w := range want {
		if read.Rows[0][i] != w {
			t.Fatalf("native value %d: %s", i, read.Rows[0][i])
		}
	}
	page, err := rt.AdvancedLoader(ctx, s.ProfileID(), db, name, contract.DataOptions{Limit: 2, Object: o})
	if err != nil || len(page.Rows) != 1 || page.Editable == nil || !*page.Editable || len(page.RowRefs) != 0 {
		t.Fatal("insert page invented existing-row identity", err)
	}
	for _, kind := range []string{"update", "delete"} {
		if _, err = rt.PreviewEdits(ctx, s.ProfileID(), db, name, []contract.DataEdit{{Kind: kind, Values: values}}); !errors.Is(err, contract.ErrUnsupported) {
			t.Fatal("unsupported existing-row mutation accepted", err)
		}
	}
	var fixture struct{ Postgres bool }
	raw, _ := os.ReadFile(os.Getenv("NYADB_TRINO_CONFIG"))
	json.Unmarshal(raw, &fixture)
	if !fixture.Postgres {
		t.Skip("memory INSERT passed; PostgreSQL omission/constraint acceptance requires --postgres")
	}
	pg := contract.NamespaceRef{ProfileID: s.ProfileID(), Catalog: "postgresql", Schema: "public"}.Object("TABLE", "insert_fixture")
	pgdb := namespaceLabel(pg.Catalog, pg.Schema)
	pglease, err := s.ReserveDataDocument(pg)
	if err != nil {
		t.Fatal(err)
	}
	defer pglease.Close()
	pr := pglease.Runtime()
	id := time.Now().UnixNano()
	idText := strconv.FormatInt(id, 10)
	edits := []contract.DataEdit{{Kind: "insert", Values: map[string]contract.DataValue{"id": {Kind: contract.Integer, Text: idText}}}, {Kind: "insert", Values: map[string]contract.DataValue{"id": {Kind: contract.Integer, Text: "-" + idText}}}, {Kind: "insert", Values: map[string]contract.DataValue{"id": {Kind: contract.Integer, Text: strconv.FormatInt(id+1, 10)}}}}
	pgPlan, err := pr.PreviewEdits(ctx, s.ProfileID(), pgdb, pg.Name, edits)
	if err != nil {
		t.Fatal(err)
	}
	pgResult, err := pr.SubmitEdits(ctx, s.ProfileID(), pgdb, pg.Name, pgPlan)
	if err == nil || pgResult == nil || pgResult.Applied != 1 || !pgResult.Incomplete || pgResult.CommitConfirmed || pgResult.Sequential.Statements[2].State != contract.DataWriteNotSent {
		t.Fatal("independent prefix/unsent suffix lost", err, pgResult)
	}
	state := pgResult.Sequential.Statements[1].State
	if !managed && state != contract.DataWriteFailed || managed && state != contract.DataWriteUnknown {
		t.Fatal("native failure disposition changed", state)
	}
	progress, valid := contract.SequentialDataWriteProgress(pgResult, 3)
	if !valid || progress.Acknowledged != 1 || progress.Unknown != managed {
		t.Fatal("receipt validator rejected original prefix")
	}
	data := run("SELECT CAST(id AS VARCHAR),note FROM " + qualified(pg) + " WHERE id IN(" + idText + "," + strconv.FormatInt(id+1, 10) + ")")
	if len(data.Rows) != 1 || data.Rows[0][1] != "native default" {
		t.Fatal("omission/default or unsent suffix changed")
	}
	if _, err = pr.PreviewEdits(ctx, s.ProfileID(), pgdb, pg.Name, edits[:1]); !errors.Is(err, contract.ErrDataCommitUncertain) {
		t.Fatal("later read cleared original write block", err)
	}
	metadata, err := pr.TableMetadata(ctx, s.ProfileID(), pgdb, pg.Name)
	if err != nil || len(metadata.Indexes) > 0 {
		t.Fatal("backing primary key became synthetic Trino identity", err)
	}
}
