package questdb

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/queryplan"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func nativeProfile(t *testing.T) (connections.Profile, string) {
	t.Helper()
	if os.Getenv("NYADB_QUESTDB_IT") != "1" {
		t.Skip("requires an explicitly owned official QuestDB fixture")
	}
	raw, err := os.ReadFile(os.Getenv("NYADB_QUESTDB_CONFIG"))
	if err != nil {
		t.Fatal("missing private QuestDB configuration")
	}
	var cfg struct {
		Host, Username, PasswordFile, Image, Version, Container string
		Port                                                    uint16
	}
	if json.Unmarshal(raw, &cfg) != nil || cfg.Host != "127.0.0.1" || cfg.Port == 0 || cfg.Image != "questdb/questdb@sha256:1e109276b61087010d8a3a0b033c313fcb26f6af898cbdc30f53e51cf494d0c5" || cfg.Version != "8.2.3" || cfg.Container == "" {
		t.Fatal("fixture must be the pinned official loopback product")
	}
	secret, err := os.ReadFile(cfg.PasswordFile)
	if err != nil {
		t.Fatal("missing private credential")
	}
	p, _ := connections.Defaults(connections.QuestDB)
	p.ID, p.Name, p.Host, p.Port, p.Username = 741, "native QuestDB", cfg.Host, cfg.Port, cfg.Username
	t.Logf("official QuestDB version=%s image=%s original_container=%s", cfg.Version, cfg.Image, cfg.Container)
	return p, string(secret)
}
func nativeSession(t *testing.T, p connections.Profile, password string) *Session {
	t.Helper()
	s, err := OpenSession(context.Background(), p, password)
	if err != nil {
		t.Fatal("native session failed", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s.(*Session)
}
func nativeLease(t *testing.T, s *Session, id uint64) contract.QueryLease {
	t.Helper()
	l, err := s.ReserveQuery(id)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(l.Release)
	return l
}
func nativeSQL(t *testing.T, l contract.QueryLease, source string) contract.SQLResult {
	t.Helper()
	r, err := l.Execute(context.Background(), "qdb", source)
	if err != nil {
		t.Fatal("native SQL failed", err)
	}
	return r
}
func nativeEvidence(t *testing.T, name string, value any) {
	t.Helper()
	output := os.Getenv("NYADB_QUESTDB_EVIDENCE")
	if output == "" {
		return
	}
	if err := os.MkdirAll(output, 0700); err != nil {
		t.Fatal(err)
	}
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(output, name+".json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
}
func nativeTableName() string { return "native_" + strconv.FormatInt(time.Now().UnixNano(), 36) }

func TestNativeQuestDBMetadataPagingAndExactTypedValues(t *testing.T) {
	p, password := nativeProfile(t)
	s := nativeSession(t, p, password)
	l := nativeLease(t, s, 1)
	table := nativeTableName()
	nativeSQL(t, l, "CREATE TABLE "+quote(table)+"(ts TIMESTAMP,sym SYMBOL INDEX,seq LONG,note STRING,nullable STRING,ip IPv4,u UUID) TIMESTAMP(ts) PARTITION BY DAY WAL")
	nativeSQL(t, l, "INSERT INTO "+quote(table)+" VALUES('2026-11-16T00:00:00.000001Z','same',9007199254740993,'NULL',NULL,'192.0.2.42','01234567-89ab-cdef-0123-456789abcdef'),('2026-11-16T00:00:00.000001Z','same',-9,'native Ω','','192.0.2.43','01234567-89ab-cdef-0123-456789abcdee')")
	deadline := time.Now().Add(15 * time.Second)
	for {
		n, err := s.count(context.Background(), p.ID, "qdb", table)
		if err == nil && n == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("WAL rows did not become visible", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	o := s.Namespace("qdb").Object("table", table)
	metadata, err := s.TableMetadata(context.Background(), o)
	if err != nil || len(metadata.Columns) != 7 || metadata.Editable || len(metadata.Indexes) != 1 || metadata.Indexes[0].Unique || metadata.Columns[0].Key != "" || metadata.Columns[1].Key != "" || !strings.Contains(metadata.Definition, "WAL") || !strings.Contains(metadata.Columns[0].Extra, "designated timestamp") {
		t.Fatal("native metadata or identity changed", err, metadata)
	}
	if s.tableMode != "matView" {
		t.Fatal("actual 8.2.3 metadata generation not detected", s.tableMode)
	}
	runtime := s.DataRuntime()
	if runtime.Saver != nil || runtime.Transactions.Begin != nil || runtime.Capabilities.EditableTables || runtime.Capabilities.Keyset {
		t.Fatal("nonunique symbol/timestamp granted row mutation")
	}
	first, err := runtime.AdvancedLoader(context.Background(), p.ID, "qdb", table, contract.DataOptions{Object: o, Limit: 1, SortColumn: "seq", SortDescending: true})
	if err != nil || len(first.Rows) != 1 || !first.HasMore || first.Rows[0][2].Value.Text != "9007199254740993" || first.Rows[0][3].Value.Kind != contract.Text || first.Rows[0][4].Value.Kind != contract.Null || first.StableOrder || !first.LiveRead || first.Editable == nil || *first.Editable {
		t.Fatal("native page changed typed values", err, first)
	}
	second, err := runtime.AdvancedLoader(context.Background(), p.ID, "qdb", table, contract.DataOptions{Object: o, Offset: 1, Limit: 1, SortColumn: "seq", SortDescending: true})
	if err != nil || len(second.Rows) != 1 || second.HasMore || second.Rows[0][2].Value.Text != "-9" || second.Rows[0][4].Value.Kind != contract.Text || second.Rows[0][4].Value.Text != "" {
		t.Fatal("native range page changed", err, second)
	}
	filtered, err := runtime.AdvancedLoader(context.Background(), p.ID, "qdb", table, contract.DataOptions{Object: o, Limit: 10, FilterColumn: "seq", FilterValue: "9007199254740993"})
	if err != nil || len(filtered.Rows) != 1 || filtered.Rows[0][2].Value.Text != "9007199254740993" {
		t.Fatal("typed pgx LONG binding lost precision", err, filtered)
	}
	var exported bytes.Buffer
	if err = runtime.JSONL(context.Background(), o, &exported); err != nil || !strings.Contains(exported.String(), "9007199254740993") || !strings.Contains(exported.String(), "native Ω") || strings.Contains(exported.String(), password) {
		t.Fatal("complete export changed", err)
	}
	if _, err = s.TableMetadata(context.Background(), contract.ObjectRef{ProfileID: p.ID + 1, Catalog: "qdb", Kind: "TABLE", Name: table}); !errors.Is(err, connections.ErrInvalid) {
		t.Fatal("foreign original identity admitted")
	}
	nativeEvidence(t, "metadata-pages-export", struct {
		Metadata                *contract.TableStructure
		First, Second, Filtered *contract.DataPage
		Export                  string
	}{metadata, first, second, filtered, exported.String()})
}

func TestNativeQuestDBOriginalSQLLeasesAndStreaming(t *testing.T) {
	p, password := nativeProfile(t)
	s := nativeSession(t, p, password)
	first := nativeLease(t, s, 2)
	second := nativeLease(t, s, 3)
	table := nativeTableName()
	nativeSQL(t, first, "CREATE TABLE "+quote(table)+"(id LONG,note STRING)")
	nativeSQL(t, first, "BEGIN; INSERT INTO "+quote(table)+" VALUES(9007199254740993,'original')")
	if r := nativeSQL(t, second, "SELECT count(*) FROM "+quote(table)); r.Sets[0].Rows[0][0].Text != "0" {
		t.Fatal("separate physical SQL lease saw an uncommitted writer")
	}
	nativeSQL(t, first, "COMMIT")
	if r := nativeSQL(t, second, "SELECT id FROM "+quote(table)); r.Sets[0].Rows[0][0].Text != "9007199254740993" {
		t.Fatal("original COMMIT did not settle")
	}
	partial, err := second.Execute(context.Background(), "qdb", "INSERT INTO "+quote(table)+" VALUES(42,'captured prefix'); SELECT missing FROM "+quote(table)+"; INSERT INTO "+quote(table)+" VALUES(99,'must not replay')")
	if err == nil || len(partial.Steps) != 2 || partial.Steps[0].State != "completed" || partial.Steps[1].State != "failed" {
		t.Fatal("original prefix receipt changed", err, partial)
	}
	if r := nativeSQL(t, second, "SELECT count(*) FROM "+quote(table)); r.Sets[0].Rows[0][0].Text != "2" {
		t.Fatal("failed prefix replayed or third statement ran")
	}
	rows := 0
	streamed, err := first.ExecuteStream(context.Background(), "qdb", "SELECT x FROM long_sequence(12000)", func(index int, cols []contract.Column, row []contract.Cell) error {
		rows++
		if index != 0 || len(cols) != 1 || row[0].Text != strconv.Itoa(rows) {
			return errors.New("original streaming ordinal changed")
		}
		return nil
	})
	if err != nil || rows != 12000 || len(streamed.Sets[0].Rows) != previewRows || !streamed.Truncated {
		t.Fatal("stream or preview limit changed", err, rows)
	}
	long := strings.Repeat("Ω", 5000)
	nativeSQL(t, second, "INSERT INTO "+quote(table)+" VALUES(3,'"+long+"')")
	preview := nativeSQL(t, second, "SELECT note FROM "+quote(table)+" WHERE id=3")
	if !preview.Sets[0].Rows[0][0].Truncated {
		t.Fatal("long preview did not retain truncation state")
	}
	var out []contract.DataValue
	err = s.DataRuntime().Exporter(context.Background(), p.ID, "qdb", table, contract.DataOptions{FilterColumn: "id", FilterValue: "3"}, []string{"note"}, func(v []contract.DataValue) error { out = append(out, v...); return nil })
	if err != nil || len(out) != 1 || out[0].Text != long {
		t.Fatal("export used truncated preview", err)
	}
	nativeEvidence(t, "original-leases-prefix-stream", struct {
		Partial          contract.SQLResult
		StreamedRows     int
		Preview          contract.SQLResult
		ExportCharacters int
	}{partial, rows, preview, len([]rune(out[0].Text))})
}

func TestNativeQuestDBPlanAndEmptyResultReceipts(t *testing.T) {
	p, password := nativeProfile(t)
	s := nativeSession(t, p, password)
	l := nativeLease(t, s, 91)
	table := nativeTableName()
	nativeSQL(t, l, "CREATE TABLE "+quote(table)+"(id LONG,note STRING); INSERT INTO "+quote(table)+" VALUES(9007199254740993,'native')")
	for _, source := range []string{
		"SELECT * FROM " + quote(table) + " WHERE id=9007199254740993",
		"SELECT sum(id) FROM " + quote(table),
		"SELECT * FROM " + quote(table) + " ORDER BY id LIMIT 1",
	} {
		q, err := queryplan.Build("questdb", source)
		if err != nil {
			t.Fatal(err)
		}
		r := nativeSQL(t, l, q)
		plan, err := queryplan.Parse("questdb", r)
		if err != nil || len(plan.Nodes) != 1 {
			t.Fatal("native plan receipt failed", err, r)
		}
	}
	r := nativeSQL(t, l, "SELECT id FROM "+quote(table)+"; SELECT id FROM "+quote(table)+" WHERE false")
	if len(r.Sets) != 2 || len(r.Sets[1].Rows) != 0 || len(r.Steps) != 2 {
		t.Fatal("empty native receipt disappeared", r)
	}
	nativeEvidence(t, "empty-original-result", r)
}

func TestNativeQuestDBCancellationRetiresOnlyOriginalLease(t *testing.T) {
	p, password := nativeProfile(t)
	s := nativeSession(t, p, password)
	original := nativeLease(t, s, 4)
	other := nativeLease(t, s, 5)
	if err := original.Preconnect(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	r, err := original.Execute(ctx, "qdb", "SELECT sum(x) FROM long_sequence(100000000)")
	if err == nil || len(r.Steps) != 1 || r.Steps[0].State != "uncertain" || !errors.Is(err, contract.ErrSQLSessionLost) {
		t.Fatal("interrupted original connection did not require explicit recovery", err, r)
	}
	if _, err = original.Execute(context.Background(), "qdb", "SELECT 99"); !errors.Is(err, contract.ErrSQLSessionLost) {
		t.Fatal("lost lease silently reconnected")
	}
	if r := nativeSQL(t, other, "SELECT 42"); r.Sets[0].Rows[0][0].Text != "42" {
		t.Fatal("other lease invalidated")
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = other.Execute(context.Background(), "qdb", "SELECT 1"); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("closed session admitted original lease")
	}
	nativeEvidence(t, "original-cancel", r)
}

func TestNativeQuestDBReadOnlyCredentialAndPoolIsolation(t *testing.T) {
	p, password := nativeProfile(t)
	bad := password + "-wrong"
	if _, err := OpenSession(context.Background(), p, bad); err == nil || strings.Contains(err.Error(), password) {
		t.Fatal("wrong credential accepted or exposed")
	}
	p.QuestDBOptions.ReadOnly = true
	p.QuestDBOptions.SQLMaxConnections = 1
	s := nativeSession(t, p, password)
	first := nativeLease(t, s, 6)
	second := nativeLease(t, s, 7)
	if _, err := first.Execute(context.Background(), "qdb", "SELECT 1; CREATE TABLE must_not_exist(id LONG)"); !errors.Is(err, contract.ErrUnsupported) {
		t.Fatal("readonly batch partially executed")
	}
	if err := first.Preconnect(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := second.Preconnect(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("physical SQL pool limit ignored", err)
	}
	if _, err := s.Browse(context.Background(), s.Namespace("qdb")); err != nil {
		t.Fatal("SQL lease starved reserved metadata capacity", err)
	}
	first.Release()
	if err := second.Preconnect(context.Background()); err != nil {
		t.Fatal("explicit slot release did not settle", err)
	}
	if r := nativeSQL(t, second, "SELECT 'UPDATE; DROP',42"); r.Sets[0].Rows[0][1].Text != "42" {
		t.Fatal("readonly literals changed")
	}
}
