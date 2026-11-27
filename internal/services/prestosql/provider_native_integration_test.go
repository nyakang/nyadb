package prestosql

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/driverstore"
	"github.com/nyakang/nyadb/internal/services/jdbcplugin"
)

type providerFixture struct{ Endpoint, Archive, SHA256, Directory, ContainerID, Nonce string }

type providerEvidence struct {
	mu        sync.Mutex
	processes []int
	receipts  []map[string]any
}

func nativeProvider(t *testing.T) (providerFixture, Provider, connections.PrestoSQLManagedConfig, *providerEvidence) {
	t.Helper()
	if os.Getenv("NYADB_PRESTOSQL_PLUGIN_IT") != "1" {
		t.Skip("explicit original PrestoSQL350 fixture required")
	}
	var f providerFixture
	raw, err := os.ReadFile(os.Getenv("NYADB_PRESTOSQL_PLUGIN_FIXTURE"))
	if err != nil || json.Unmarshal(raw, &f) != nil {
		t.Fatal("original provider fixture unavailable")
	}
	u, err := url.Parse(f.Endpoint)
	if err != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" {
		t.Fatal("owned loopback fixture required")
	}
	store, err := driverstore.New(filepath.Join(f.Directory, t.Name()+"-store"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	archive, err := os.Open(f.Archive)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := store.Install(ctx, archive, f.SHA256)
	archive.Close()
	if err != nil {
		t.Fatal(err)
	}
	evidence := &providerEvidence{}
	provider := Provider{Store: store}
	provider.Started = func(pid int) error {
		evidence.mu.Lock()
		defer evidence.mu.Unlock()
		evidence.processes = append(evidence.processes, pid)
		file, err := os.OpenFile(filepath.Join(f.Directory, "go-provider-processes.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		defer file.Close()
		return json.NewEncoder(file).Encode(map[string]any{"test": t.Name(), "pid": pid})
	}
	provider.Observe = func(method string, receipt jdbcplugin.Receipt) {
		raw := []byte(receipt.OriginalResponse())
		digest := sha256.Sum256(raw)
		evidence.mu.Lock()
		defer evidence.mu.Unlock()
		evidence.receipts = append(evidence.receipts, map[string]any{"method": method, "receipt": receipt.Clone(), "native_bytes": raw, "native_sha256": hex.EncodeToString(digest[:]), "cancellation_bytes": []byte(receipt.CancellationResponse())})
	}
	t.Cleanup(func() {
		evidence.mu.Lock()
		defer evidence.mu.Unlock()
		raw, err := json.MarshalIndent(map[string]any{"processes": evidence.processes, "receipts": evidence.receipts}, "", "  ")
		if err != nil || os.WriteFile(filepath.Join(f.Directory, t.Name()+"-receipts.json"), raw, 0600) != nil {
			t.Error("provider receipt evidence unavailable", err)
		}
	})
	config := connections.PrestoSQLManagedConfig{Endpoint: f.Endpoint, Username: "nyadb_provider", Catalog: "memory", Schema: "default", DriverVersion: manifest.Version, DriverSHA256: f.SHA256, MaxQueryProcesses: 2, PoolWaitMillis: 250, ConnectTimeoutSeconds: 30, JDBCParameters: "source=nyadb_provider"}
	return f, provider, config, evidence
}

func queryNative(t *testing.T, q contract.QueryLease, namespace, sql string) contract.SQLResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	r, err := q.Execute(ctx, namespace, sql)
	if err != nil {
		t.Fatal("original provider query", err)
	}
	if len(r.Steps) == 0 || r.NativeJSON == "" {
		t.Fatal("original query outcomes/receipts missing")
	}
	return r
}

func TestPrestoSQLPrivateProviderNativeOwnershipMetadataAndPartialBatch(t *testing.T) {
	f, provider, config, evidence := nativeProvider(t)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	s, err := provider.OpenManaged(ctx, 64, config, Credentials{})
	if err != nil {
		t.Fatal("original network-validated session", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	q1, err := s.ReserveQuery(1)
	if err != nil {
		t.Fatal(err)
	}
	q2, err := s.ReserveQuery(2)
	if err != nil {
		t.Fatal(err)
	}
	if err = q1.Preconnect(ctx); err != nil {
		t.Fatal(err)
	}
	if err = q2.Preconnect(ctx); err != nil {
		t.Fatal(err)
	}
	q3, err := s.ReserveQuery(3)
	if err != nil {
		t.Fatal(err)
	}
	queued, stop := context.WithCancel(ctx)
	stop()
	if err = q3.Preconnect(queued); !errors.Is(err, context.Canceled) {
		t.Fatal("queued cancellation changed ownership", err)
	}
	evidence.mu.Lock()
	started := len(evidence.processes)
	evidence.mu.Unlock()
	if started != 3 {
		t.Fatal("query slots launched extra executor", started)
	}
	// Metadata has its own captured process and remains usable with both
	// workload slots occupied. Catalog/schema labels are not identity parsers.
	namespaces, err := s.Namespaces(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var selected contract.Namespace
	for _, n := range namespaces {
		if n.Ref.Catalog == "memory" && n.Ref.Schema == "default" {
			selected = n
		}
	}
	if selected.Ref.ProfileID != 64 || s.Namespace(selected.Label) != selected.Ref {
		t.Fatal("qualified original namespace lost")
	}
	label := selected.Label
	queryNative(t, q1, label, `CREATE TABLE memory.default."provider_%猫" (id BIGINT, amount DECIMAL(38,20), payload VARBINARY, note VARCHAR); CREATE TABLE memory.default."provider_ax猫" (foreign_column VARCHAR)`)
	objects, err := s.Browse(ctx, selected.Ref)
	if err != nil {
		t.Fatal(err)
	}
	object := selected.Ref.Object("table", "provider_%猫")
	found := false
	for _, o := range objects {
		if o.Ref == object {
			found = true
		}
	}
	if !found {
		t.Fatal("original wildcard/Unicode table not browsed")
	}
	structure, err := s.TableMetadata(ctx, object)
	if err != nil || len(structure.Columns) != 4 || structure.Columns[0].Name != "id" || structure.Columns[1].NumericPrecision == nil || *structure.Columns[1].NumericPrecision != 38 || *structure.Columns[1].NumericScale != 20 || structure.Columns[2].ValueKind != contract.Binary || structure.Editable {
		t.Fatal("exact qualified original column metadata lost", err)
	}
	foreign := selected.Ref
	foreign.ProfileID = 65
	if _, err = s.Browse(ctx, foreign); !errors.Is(err, connections.ErrInvalid) {
		t.Fatal("foreign profile metadata accepted", err)
	}
	queryNative(t, q1, label, "SET SESSION query_max_run_time='17m'")
	first := queryNative(t, q1, label, "SHOW SESSION LIKE 'query_max_run_time'")
	second := queryNative(t, q2, label, "SHOW SESSION LIKE 'query_max_run_time'")
	if len(first.Sets) != 1 || len(first.Sets[0].Rows) != 1 || len(second.Sets[0].Rows) != 1 || first.Sets[0].Rows[0][1].Text == second.Sets[0].Rows[0][1].Text || !strings.HasPrefix(first.Sets[0].Rows[0][1].Text, "17") {
		t.Fatal("independent original JDBC session settings leaked")
	}
	// Native memory INSERT completes, UPDATE is unsupported. The first ACK
	// remains a completed step; the batch is never described as rolled back.
	partial, err := q1.Execute(ctx, label, `INSERT INTO memory.default."provider_%猫" VALUES (9223372036854775807,DECIMAL '9007199254740993.12345678901234567890',X'00ff80',NULL); UPDATE memory.default."provider_%猫" SET note='secret_provider_value64'`)
	if !errors.Is(err, jdbcplugin.ErrRemote) || len(partial.Steps) != 2 || partial.Steps[0].State != "completed" || partial.Steps[0].AffectedRows != 1 || partial.Steps[1].State != "failed" || strings.Contains(err.Error(), "secret_provider_value64") || partial.AffectedRows != 1 {
		t.Fatal("partial native batch receipts lost", err, partial.Steps)
	}
	values := queryNative(t, q2, label, `SELECT * FROM memory.default."provider_%猫"`)
	row := values.Sets[0].Rows[0]
	if row[0].Text != "9223372036854775807" || row[1].Text != "9007199254740993.1234567890123456789" || row[1].Kind != contract.Decimal || row[2].Kind != contract.Text || row[2].Text != "0x00ff80" || row[3].Kind != contract.Null {
		t.Fatal("native result precision or untyped binary boundary lost", row)
	}
	retained := values.NativeJSON
	sinkErr := errors.New("consumer stopped")
	sunk, err := q1.ExecuteStream(ctx, label, `SELECT 1; INSERT INTO memory.default."provider_%猫" (id) VALUES (99)`, func(_ int, cols []contract.Column, cells []contract.Cell) error {
		cols[0].Name = "caller"
		cells[0].Text = "caller"
		return sinkErr
	})
	if !errors.Is(err, sinkErr) || len(sunk.Steps) != 1 || sunk.Steps[0].State != "completed" {
		t.Fatal("consumer failure dispatched later statement", err)
	}
	count := queryNative(t, q2, label, `SELECT count(*) FROM memory.default."provider_%猫"`)
	if count.Sets[0].Rows[0][0].Text != "1" || retained != values.NativeJSON {
		t.Fatal("later operation replaced original data or replayed batch")
	}
	q2.Release()
	if err = q3.Preconnect(ctx); err != nil {
		t.Fatal("released slot unavailable", err)
	}
	old := q1
	q1, err = q1.Replace()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = old.Execute(ctx, label, "SELECT 99"); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("released original lease executed", err)
	}
	replaced := queryNative(t, q1, label, "SHOW SESSION LIKE 'query_max_run_time'")
	if replaced.Sets[0].Rows[0][1].Text != second.Sets[0].Rows[0][1].Text {
		t.Fatal("explicit replacement retained foreign original session state")
	}
	queryNative(t, q1, label, `DROP TABLE memory.default."provider_%猫"; DROP TABLE memory.default."provider_ax猫"`)
	if err = s.Close(); err != nil || s.Alive() {
		t.Fatal("original session cleanup", err)
	}
	if _, err = s.ReserveQuery(9); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("closed session recovered implicitly")
	}
	if err = provider.Store.Remove(ctx, "prestosql", config.DriverVersion); err != nil {
		t.Fatal("original package marker survived", err)
	}
	proof := map[string]any{"namespaces": namespaces, "objects": objects, "structure": structure, "partial_batch": partial, "values": values, "first_session_setting": first, "second_session_setting": second, "replacement_setting": replaced, "query_slots_preserved": true, "consumer_stopped_before_later_statement": true, "closed_package_removed": true}
	raw, err := json.MarshalIndent(proof, "", "  ")
	if err != nil || os.WriteFile(filepath.Join(f.Directory, "provider-ownership.json"), raw, 0600) != nil {
		t.Fatal("provider evidence", err)
	}
}
