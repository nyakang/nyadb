package trino

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func nativeProfile(t *testing.T) (connections.Profile, string) {
	t.Helper()
	cfg, secret := nativeConfig(t)
	u, err := url.Parse(cfg.Endpoint)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.ParseUint(u.Port(), 10, 16)
	if err != nil {
		t.Fatal(err)
	}
	p, _ := connections.Defaults(connections.Trino)
	p.ID, p.Name, p.Host, p.Port, p.Username, p.Database = 71, "native workspace", u.Hostname(), uint16(port), cfg.Username, "memory"
	p.TrinoOptions.DefaultSchema, p.TrinoOptions.CAFile = "default", cfg.CAFile
	if u.Scheme == "http" {
		p.TLSMode = "disable"
	}
	return p, secret
}

func TestTrinoNativeWorkspaceMetadataPagingTypesExportAndRecovery(t *testing.T) {
	p, secret := nativeProfile(t)
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	raw, err := OpenSession(ctx, p, secret)
	if err != nil {
		t.Fatal(err)
	}
	s := raw.(*Session)
	defer s.Close()
	names, err := s.Namespaces(ctx)
	if err != nil {
		t.Fatal(err)
	}
	label := namespaceLabel("memory", "default")
	if len(names) < 3 || names[0].Label != label {
		t.Fatal("preferred qualified namespace lost")
	}
	if s.Namespace(namespaceLabel("memory", "information_schema")) == s.Namespace(namespaceLabel("system", "information_schema")) {
		t.Fatal("same-named schemas collided across catalogs")
	}
	if s.Namespace(label).Catalog != "memory" || s.Namespace(namespaceLabel("system", "runtime")).Schema != "runtime" || s.Namespace("default").Schema != "" {
		t.Fatal("ambiguous namespace admitted")
	}
	lease, err := s.ReserveQuery(1)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	table := fmt.Sprintf("nyadb_workspace_%d", time.Now().UnixNano())
	qname := "memory.default." + table
	run := func(q string) contract.SQLResult {
		t.Helper()
		r, e := lease.Execute(ctx, label, q)
		if e != nil {
			t.Fatal(e)
		}
		return r
	}
	run("CREATE TABLE " + qname + "(id BIGINT,note VARCHAR,amount DECIMAL(38,18),payload VARBINARY,stamp TIMESTAMP(9),items ARRAY(BIGINT),m MAP(VARCHAR,BIGINT),r ROW(n BIGINT,s VARCHAR),j JSON)")
	defer func() { _, _ = lease.Execute(context.Background(), label, "DROP TABLE IF EXISTS "+qname) }()
	run("INSERT INTO " + qname + " SELECT 9007199254740993,'中文',DECIMAL '12345678901234567890.123456789012345678',X'00FF',TIMESTAMP '2026-11-08 01:02:03.123456789',ARRAY[9007199254740993],MAP(ARRAY['x'],ARRAY[9007199254740993]),CAST(ROW(9007199254740993,'中文') AS ROW(n BIGINT,s VARCHAR)),JSON '{\"n\":9007199254740993}'")
	run("INSERT INTO " + qname + " SELECT 2,'',NULL,X'',NULL,NULL,NULL,NULL,NULL")
	run("INSERT INTO " + qname + " SELECT 3,array_join(repeat('长',3000),''),NULL,NULL,NULL,NULL,NULL,NULL,NULL")
	objects, err := s.Browse(ctx, s.Namespace(label))
	if err != nil {
		t.Fatal(err)
	}
	var o contract.ObjectRef
	for _, v := range objects {
		if v.Name == table {
			o = v.Ref
		}
	}
	if o.ProfileID != p.ID || o.Catalog != "memory" || o.Schema != "default" {
		t.Fatal("qualified object lost")
	}
	structure, err := s.TableMetadata(ctx, o)
	if err != nil || structure == nil || len(structure.Columns) != 9 || structure.Editable || !strings.Contains(structure.Definition, table) {
		t.Fatal("native structure failed", err)
	}
	reads := s.DataReads()
	options := contract.DataOptions{Object: o, Limit: 2, SortColumn: "id", SortDescending: true}
	page, err := reads.AdvancedLoader(ctx, p.ID, label, table, options)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Rows) != 2 || !page.HasMore || page.Object != o || page.Rows[0][0].Value.Text != "9007199254740993" || page.Rows[0][2].Value.Kind != contract.Decimal || page.Rows[0][2].Value.Text != "12345678901234567890.123456789012345678" || !bytes.Equal(page.Rows[0][3].Value.Bytes, []byte{0, 255}) || page.Rows[0][4].Value.Text != "2026-11-08 01:02:03.123456789" || page.Rows[0][5].Value.Text != "[9007199254740993]" || !page.Rows[1][1].Truncated {
		t.Fatal("native page types/precision/truncation failed")
	}
	options.Offset = 2
	page, err = reads.AdvancedLoader(ctx, p.ID, label, table, options)
	if err != nil || len(page.Rows) != 1 || page.HasMore || page.Rows[0][1].Null || page.Rows[0][1].Text != "" || !page.Rows[0][2].Null || page.Rows[0][3].Value.Bytes == nil {
		t.Fatal("offset page null/empty identity failed", err)
	}
	options.Offset = 0
	options.FilterColumn = "id"
	options.FilterMode = "equal"
	options.FilterValue = "9007199254740993"
	page, err = reads.AdvancedLoader(ctx, p.ID, label, table, options)
	if err != nil || len(page.Rows) != 1 || strings.Contains(page.QuerySQL, options.FilterValue) {
		t.Fatal("native bound filtering failed", err)
	}
	options.FilterColumn = "note"
	options.FilterValue = "中文' OR true --"
	page, err = reads.AdvancedLoader(ctx, p.ID, label, table, options)
	if err != nil || len(page.Rows) != 0 {
		t.Fatal("filter quoting admitted injection", err)
	}
	options = contract.DataOptions{Object: o, SortColumn: "id"}
	var exported [][]contract.DataValue
	err = s.DataExports().Exporter(ctx, p.ID, label, table, options, []string{"id", "note", "amount"}, func(v []contract.DataValue) error { exported = append(exported, v); return nil })
	if err != nil || len(exported) != 3 || len(exported[1][1].Text) != 9000 || exported[2][0].Text != "9007199254740993" {
		t.Fatal("complete typed export failed", err)
	}
	var jsonl bytes.Buffer
	if err = s.DataExports().JSONL(ctx, o, &jsonl); err != nil || bytes.Count(jsonl.Bytes(), []byte("\n")) != 3 || !strings.Contains(jsonl.String(), "9007199254740993") {
		t.Fatal("JSONL failed", err)
	}
	if count, e := reads.Count(ctx, p.ID, label, table); e != nil || count != 3 {
		t.Fatal("count failed", e)
	}
	options.Object.Schema = "runtime"
	if _, err = reads.AdvancedLoader(ctx, p.ID, label, table, options); !errors.Is(err, connections.ErrInvalid) {
		t.Fatal("foreign qualified identity accepted", err)
	}
	r := run("SELECT 9007199254740993; SELECT CAST(NULL AS DECIMAL(38,18)) WHERE FALSE")
	if len(r.Sets) != 2 || len(r.Sets[1].Columns) != 1 || len(r.Sets[1].Rows) != 0 {
		t.Fatal("multi or empty typed result lost")
	}
	r = run("USE tpch.tiny;SELECT current_catalog,current_schema,COUNT(*) FROM nation GROUP BY 1,2")
	if r.Sets[0].Rows[0][0].Text != "tpch" {
		t.Fatal("native session USE lost")
	}
	// A UI namespace change switches this lease's headers; an unchanged selection
	// continues explicit USE state. Each document owns a separate logical session.
	other, e := s.ReserveQuery(2)
	if e != nil {
		t.Fatal(e)
	}
	defer other.Release()
	r, e = other.Execute(ctx, label, "SELECT current_catalog,current_schema")
	if e != nil || r.Sets[0].Rows[0][0].Text != "memory" {
		t.Fatal("logical sessions leaked", e)
	}
	_, err = other.Execute(ctx, label, "SELECT unknown_column")
	var queryErr *QueryError
	if !errors.As(err, &queryErr) {
		t.Fatal("terminal error lost", err)
	}
	if _, err = other.Execute(ctx, label, "SELECT 1"); err != nil {
		t.Fatal("confirmed SQL error retired healthy session", err)
	}
	cancelCtx, cancelQuery := context.WithTimeout(ctx, 1200*time.Millisecond)
	_, err = other.Execute(cancelCtx, label, "SELECT COUNT(*) FROM tpch.sf100.lineitem a CROSS JOIN tpch.sf100.lineitem b")
	cancelQuery()
	if !errors.Is(err, contract.ErrSQLSessionLost) {
		t.Fatal("uncertain cancellation did not require recovery", err)
	}
	if _, err = other.Execute(ctx, label, "SELECT 2"); !errors.Is(err, contract.ErrSQLSessionLost) {
		t.Fatal("cancelled lease replayed", err)
	}
	recovered, err := other.Replace()
	if err != nil {
		t.Fatal(err)
	}
	defer recovered.Release()
	if _, err = recovered.Execute(ctx, label, "SELECT 3"); err != nil {
		t.Fatal("explicit recovery failed", err)
	}
}
