package agentbridge_test

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/sqltext"
	"github.com/nyakang/nyadb/internal/services/agentbridge"
	"github.com/nyakang/nyadb/internal/services/driverstore"
	"github.com/nyakang/nyadb/internal/services/trino"
)

type trinoAgentFixture struct {
	Archive           string `json:"archive"`
	SHA256            string `json:"sha256"`
	DriverVersion     string `json:"driver_version"`
	Directory         string `json:"directory"`
	Endpoint          string `json:"endpoint"`
	Username          string `json:"username"`
	PasswordFile      string `json:"password_file"`
	TrustFile         string `json:"trust_file"`
	TrustPasswordFile string `json:"trust_password_file"`
}

func realTrinoAgentFixture(t *testing.T) trinoAgentFixture {
	t.Helper()
	if os.Getenv("NYADB_AGENT_TRINO_IT") != "1" {
		t.Skip("requires owned original Trino/JDBC 471 and official Trino 484 fixture")
	}
	raw, err := os.ReadFile(os.Getenv("NYADB_AGENT_TRINO_FIXTURE_FILE"))
	var f trinoAgentFixture
	if err != nil || json.Unmarshal(raw, &f) != nil || f.Archive == "" || f.Directory == "" || f.Endpoint == "" || f.DriverVersion == "" {
		t.Fatal("private original Trino fixture unavailable")
	}
	return f
}

func (f trinoAgentFixture) params(t *testing.T) map[string]any {
	t.Helper()
	endpoint, err := url.Parse(f.Endpoint)
	if err != nil {
		t.Fatal("invalid owned endpoint")
	}
	secret, err := os.ReadFile(f.PasswordFile)
	if err != nil {
		t.Fatal("private fixture credential unavailable")
	}
	options := url.Values{}
	if endpoint.Scheme == "https" {
		trustPassword, err := os.ReadFile(f.TrustPasswordFile)
		if err != nil {
			t.Fatal("private trust credential unavailable")
		}
		options.Set("SSL", "true")
		options.Set("SSLTrustStorePath", f.TrustFile)
		options.Set("SSLTrustStorePassword", string(trustPassword))
		options.Set("SSLTrustStoreType", "PKCS12")
	}
	connection := "jdbc:trino://" + endpoint.Host + "/memory/default"
	if len(options) > 0 {
		connection += "?" + options.Encode()
	}
	return map[string]any{"connection_string": connection, "database": "memory", "username": f.Username, "password": string(secret), "sessionRole": "workload"}
}

func realTrinoRuntime(t *testing.T, f trinoAgentFixture) (*driverstore.Store, driverstore.Manifest, *driverstore.Runtime) {
	t.Helper()
	store, err := driverstore.New(filepath.Join(f.Directory, "store"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	lease, err := store.Acquire(ctx, "trino", f.DriverVersion, f.SHA256)
	if errors.Is(err, driverstore.ErrMissing) {
		input, openErr := os.Open(f.Archive)
		if openErr != nil {
			t.Fatal(openErr)
		}
		_, err = store.Install(ctx, input, f.SHA256)
		input.Close()
		if err != nil {
			t.Fatalf("original immutable package import failed: %v", err)
		}
		lease, err = store.Acquire(ctx, "trino", f.DriverVersion, f.SHA256)
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { lease.Close() })
	m := lease.Manifest()
	if m.Key != "trino" || m.MainClass != "com.dbx.agent.trino.TrinoAgent" || m.Version != f.DriverVersion {
		t.Fatal("original managed product/version identity changed")
	}
	spec, err := lease.LaunchSpec("trino")
	if err != nil {
		t.Fatal(err)
	}
	observe := func(pid int) error {
		stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
		if err != nil {
			return err
		}
		fields := strings.Fields(string(stat[strings.LastIndexByte(string(stat), ')')+1:]))
		if len(fields) < 20 || len(spec.Args) < 2 || spec.Args[0] != "-cp" {
			return errors.New("original native process identity unavailable")
		}
		record, err := json.Marshal(map[string]any{"pid": pid, "start_ticks": fields[19], "java": spec.Executable, "agent": strings.Split(spec.Args[1], string(os.PathListSeparator))[0]})
		if err != nil {
			return err
		}
		journal, err := os.OpenFile(filepath.Join(f.Directory, "processes.jsonl"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
		if err != nil {
			return err
		}
		defer journal.Close()
		_, err = journal.Write(append(record, '\n'))
		return err
	}
	// Cold original JDBC allocation uses the product's pool wait. Queued
	// cancellation is tested separately with its own short request deadline.
	runtime, err := lease.StartJDBCObserved(ctx, "trino", driverstore.JDBCSettings{MaxConnections: 6, MetadataReserve: 1, WaitTimeout: 30 * time.Second}, agentbridge.Options{CancelWait: 15 * time.Second}, observe)
	if err != nil {
		lease.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := runtime.Close(); err != nil {
			t.Error(err)
		}
	})
	return store, m, runtime
}

func realTrinoSession(t *testing.T, c *agentbridge.Client, params map[string]any) *agentbridge.Session {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	s, receipt, err := c.OpenSession(ctx, params)
	if err != nil || !receipt.Dispatched || !receipt.ExecutorAcknowledged || !s.Alive() {
		t.Fatalf("original JDBC session open failed: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func realTrinoCall(t *testing.T, s *agentbridge.Session, method string, params any) agentbridge.Receipt {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	r, err := s.Call(ctx, method, params)
	if err != nil || !r.Dispatched || !r.ExecutorAcknowledged || r.RemoteError != nil || len(r.Result) == 0 {
		t.Fatalf("original native %s receipt missing: %v", method, err)
	}
	return r
}

func realTrinoQuery(t *testing.T, s *agentbridge.Session, source string) agentbridge.QuerySnapshot {
	t.Helper()
	r := realTrinoCall(t, s, "execute_query", map[string]any{"sql": source, "maxRows": 100, "timeoutSecs": 15})
	q, err := agentbridge.DecodeQuery(r, trino.DecodeJDBCValue)
	if err != nil || string(q.Native) != string(r.Result) || q.SQL.NativeJSON != string(r.Result) {
		t.Fatalf("original native query projection failed: %v", err)
	}
	return q
}

func realTrinoScalar(t *testing.T, s *agentbridge.Session, source string) string {
	t.Helper()
	q := realTrinoQuery(t, s, source)
	if len(q.SQL.Sets) != 1 || len(q.SQL.Sets[0].Rows) != 1 || len(q.SQL.Sets[0].Rows[0]) != 1 {
		t.Fatal("original native scalar shape changed")
	}
	return q.SQL.Sets[0].Rows[0][0].Text
}

func TestTrinoAgentNativeManagedValuesMetadataAndOriginalPackage(t *testing.T) {
	f := realTrinoAgentFixture(t)
	store, m, r := realTrinoRuntime(t, f)
	s := realTrinoSession(t, r.Client, f.params(t))
	info := realTrinoCall(t, s, "connection_info", map[string]any{})
	var identity struct {
		DatabaseInfo map[string]string `json:"databaseInfo"`
	}
	if json.Unmarshal(info.Result, &identity) != nil || !strings.Contains(identity.DatabaseInfo["driverVersion"], "471") || realTrinoScalar(t, s, "SELECT version()") != "484" {
		t.Fatal("actual managed JDBC/server identity differs from fixture pins")
	}
	query := realTrinoQuery(t, s, `SELECT CAST(9007199254740993 AS BIGINT), CAST('123456789012345678.12345678901234567890' AS DECIMAL(38,20)), false, CAST(NULL AS VARCHAR), 'NULL', '中文', X'00ff', ARRAY[CAST(9007199254740993 AS BIGINT)], MAP(ARRAY['中文'], ARRAY[CAST(9007199254740993 AS BIGINT)]), CAST(ROW(9007199254740993, '中文') AS ROW(n BIGINT, t VARCHAR)), JSON '{"n":9007199254740993}', TIMESTAMP '2026-11-16 01:02:03.123456789123', TIME '01:02:03.123456789', infinity(), nan()`)
	if len(query.SQL.Sets) != 1 || len(query.SQL.Sets[0].Rows) != 1 || len(query.SQL.Sets[0].Rows[0]) != 15 {
		t.Fatal("original typed native row shape lost")
	}
	row := query.SQL.Sets[0].Rows[0]
	if row[0].Kind != contract.Integer || row[0].Text != "9007199254740993" || row[1].Kind != contract.Decimal || row[1].Text != "123456789012345678.12345678901234567890" || row[2].Kind != contract.Boolean || row[2].Text != "false" || row[3].Kind != contract.Null || row[4].Text != "NULL" || row[5].Text != "中文" || row[10].Kind != contract.JSON || row[10].Text != `{"n":9007199254740993}` || row[13].Text != "Infinity" || row[14].Text != "NaN" {
		t.Fatal("original exact JDBC scalar values changed")
	}
	for _, index := range []int{6, 7, 8, 9} {
		if row[index].Kind != contract.Text || row[index].Bytes != nil || !row[index].Truncated || row[index].Text == "" {
			t.Fatal("lossy JDBC binary/composite rendering became complete data")
		}
	}
	if !strings.HasPrefix(row[6].Text, "[B@") || !strings.Contains(row[8].Text, "中文=9007199254740993") || !row[11].Truncated || !row[12].Truncated {
		t.Fatal("observed JDBC loss was not preserved and flagged")
	}
	t.Logf("Original JDBC types=%v; incomplete binary/array/map/row/timestamp/time=%q/%q/%q/%q/%q/%q", query.SQL.Sets[0].Columns, row[6].Text, row[7].Text, row[8].Text, row[9].Text, row[11].Text, row[12].Text)
	catalogs := realTrinoCall(t, s, "list_databases", map[string]any{})
	for _, name := range []string{"memory", "tpch", "system"} {
		if !strings.Contains(string(catalogs.Result), `"`+name+`"`) {
			t.Fatal("original qualified catalog missing")
		}
	}
	schema := "owned_" + strings.ToLower(rand.Text())
	table := `表"exact`
	quote := func(name string) string { return `"` + strings.ReplaceAll(name, `"`, `""`) + `"` }
	qualified := `"memory".` + quote(schema) + "." + quote(table)
	realTrinoQuery(t, s, "CREATE SCHEMA memory."+quote(schema))
	realTrinoQuery(t, s, "CREATE TABLE "+qualified+" (id BIGINT, amount DECIMAL(38,20), note VARCHAR)")
	schemas := realTrinoCall(t, s, "list_schemas", map[string]any{"database": "memory"})
	if !strings.Contains(string(schemas.Result), schema) {
		t.Fatal("original schema missing")
	}
	objects := realTrinoCall(t, s, "list_tables", map[string]any{"database": "memory", "schema": schema})
	var tables []struct{ Name string }
	if json.Unmarshal(objects.Result, &tables) != nil || len(tables) != 1 || tables[0].Name != table {
		t.Fatal("original table qualified identity lost")
	}
	columns := realTrinoCall(t, s, "get_columns", map[string]any{"database": "memory", "schema": schema, "table": table})
	if !strings.Contains(string(columns.Result), "decimal(38,20)") {
		t.Fatal("native numeric precision/scale missing")
	}
	ddl := realTrinoCall(t, s, "get_table_ddl", map[string]any{"database": "memory", "schema": schema, "table": table})
	var definition string
	if json.Unmarshal(ddl.Result, &definition) != nil || definition != realTrinoScalar(t, s, "SHOW CREATE TABLE "+qualified) {
		t.Fatal("original full SHOW CREATE TABLE definition was replaced")
	}
	tpchSchemas := realTrinoCall(t, s, "list_schemas", map[string]any{"database": "tpch"})
	if !strings.Contains(string(tpchSchemas.Result), `"tiny"`) {
		t.Fatal("explicit metadata catalog did not select native TPCH schemas")
	}
	// The original wire calls a catalog "database". Carry it on every
	// metadata request rather than relying on an earlier pooled operation.
	memorySchemas := realTrinoCall(t, s, "list_schemas", map[string]any{"database": "memory"})
	if !strings.Contains(string(memorySchemas.Result), schema) || strings.Contains(string(memorySchemas.Result), `"tiny"`) {
		t.Fatal("original metadata catalog selection leaked between requests")
	}
	if err := store.Remove(context.Background(), m.Key, m.Version); !errors.Is(err, driverstore.ErrBusy) {
		t.Fatal("original native runtime failed to pin complete package")
	}
	s.Close()
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-r.Client.Exited():
	default:
		t.Fatal("package released before original process exit")
	}
	if err := store.Remove(context.Background(), m.Key, m.Version); err != nil {
		t.Fatal(err)
	}
}

func realTrinoPool(t *testing.T, f trinoAgentFixture, c *agentbridge.Client) (*agentbridge.QueryPool, contract.QueryLease, contract.QueryLease) {
	t.Helper()
	pool, err := agentbridge.NewQueryPool(c, f.params(t), agentbridge.QueryConfig{InitializeSQL: `USE "memory"."default"`, MaxLeases: 2, MaxRows: 100, TimeoutSeconds: 30, Split: sqltext.SplitPortable, Decoder: trino.DecodeJDBCValue})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	first, err := pool.ReserveQuery(1)
	if err != nil {
		t.Fatal(err)
	}
	second, err := pool.ReserveQuery(2)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(first.Release)
	t.Cleanup(second.Release)
	return pool, first, second
}

func realTrinoExecute(t *testing.T, l contract.QueryLease, source string) contract.SQLResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	q, err := l.Execute(ctx, "", source)
	if err != nil || q.NativeJSON == "" || len(q.Steps) == 0 {
		t.Fatalf("original SQL document execution failed: %v", err)
	}
	return q
}

func TestTrinoAgentNativePhysicalAffinityCursorsAndIndependentSessions(t *testing.T) {
	f := realTrinoAgentFixture(t)
	_, _, r := realTrinoRuntime(t, f)
	_, first, second := realTrinoPool(t, f, r.Client)
	realTrinoExecute(t, first, "USE tpch.tiny; PREPARE exact_state FROM SELECT current_catalog, current_schema")
	q := realTrinoExecute(t, first, "EXECUTE exact_state")
	if q.Sets[0].Rows[0][0].Text != "tpch" || q.Sets[0].Rows[0][1].Text != "tiny" {
		t.Fatal("stateful JDBC document affinity changed")
	}
	q = realTrinoExecute(t, second, "SELECT current_catalog, current_schema")
	if q.Sets[0].Rows[0][0].Text != "memory" || q.Sets[0].Rows[0][1].Text != "default" {
		t.Fatal("physical catalog/schema leaked between original documents")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if _, err := second.Execute(ctx, "", "EXECUTE exact_state"); err == nil {
		t.Fatal("prepared state leaked to an independent physical lease")
	}
	s := realTrinoSession(t, r.Client, f.params(t))
	other := realTrinoSession(t, r.Client, f.params(t))
	realTrinoQuery(t, s, "USE memory.default")
	cursor, page, err := s.OpenQueryCursor(ctx, agentbridge.QueryCursorOptions{SQL: "SELECT nationkey, name FROM tpch.tiny.nation ORDER BY nationkey LIMIT 5", PageSize: 2, MaxRows: 10, TimeoutSeconds: 15, Decoder: trino.DecodeJDBCValue})
	if err != nil || !page.HasMore || len(page.SQL.Sets[0].Rows) != 2 || page.SQL.Sets[0].Rows[0][0].Text != "0" {
		t.Fatalf("original Trino cursor failed: %v", err)
	}
	t.Cleanup(func() { cursor.Close() })
	var identity struct {
		ID string `json:"session_id"`
	}
	if json.Unmarshal(page.Native, &identity) != nil || identity.ID == "" {
		t.Fatal("complete original cursor ID missing")
	}
	if _, err := other.Call(ctx, "fetch_query_page", map[string]any{"sessionId": identity.ID, "pageSize": 2}); err == nil {
		t.Fatal("cursor transferred to another logical session")
	}
	page, err = cursor.Fetch(ctx, 2)
	if err != nil || len(page.SQL.Sets[0].Rows) != 2 || page.SQL.Sets[0].Rows[0][0].Text != "2" || !page.Receipt.ExecutorAcknowledged {
		t.Fatal("original cursor continuation lost")
	}
	page, err = cursor.Fetch(ctx, 2)
	if err != nil || len(page.SQL.Sets[0].Rows) != 1 || page.SQL.Sets[0].Rows[0][0].Text != "4" || page.HasMore {
		t.Fatal("original cursor terminal page lost")
	}
	if _, err = cursor.Fetch(ctx, 2); !errors.Is(err, io.EOF) {
		t.Fatal("terminal cursor implicitly reexecuted")
	}
	if err = cursor.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = cursor.Fetch(ctx, 1); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("closed original cursor remained usable")
	}
}

func TestTrinoAgentNativeConnectorReceiptsFailureAndExplicitRecovery(t *testing.T) {
	f := realTrinoAgentFixture(t)
	_, _, r := realTrinoRuntime(t, f)
	pool, first, second := realTrinoPool(t, f, r.Client)
	table := "memory.default.owned_" + strings.ToLower(rand.Text())
	realTrinoExecute(t, first, "CREATE TABLE "+table+" (id BIGINT, note VARCHAR)")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	q, err := first.Execute(ctx, "", "INSERT INTO "+table+" VALUES(1,'原始'); SELECT missing_original_column; INSERT INTO "+table+" VALUES(2,'suffix')")
	if err == nil || len(q.Steps) != 2 || q.Steps[0].State != "completed" || q.Steps[0].AffectedRows != 1 || q.NativeJSON == "" {
		t.Fatal("native partial completion/suffix boundary was lost")
	}
	receipted := first.(interface{ OriginalQueryReceipts() []agentbridge.Receipt })
	receipts := receipted.OriginalQueryReceipts()
	if len(receipts) != 2 || !receipts[0].ExecutorAcknowledged || receipts[0].RemoteError != nil || receipts[1].RemoteError == nil || !receipts[1].ExecutorAcknowledged {
		t.Fatal("original native completed/error receipts missing")
	}
	original := string(receipts[0].Result)
	receipts[0].Result[0] = ' '
	if string(receipted.OriginalQueryReceipts()[0].Result) != original {
		t.Fatal("caller mutated original native receipt")
	}
	if count := realTrinoExecute(t, second, "SELECT count(*) FROM "+table).Sets[0].Rows[0][0].Text; count != "1" {
		t.Fatal("failed request replayed completed write or dispatched suffix")
	}
	for _, source := range []string{"UPDATE tpch.tiny.nation SET name='changed' WHERE nationkey=1", "DELETE FROM tpch.tiny.nation WHERE nationkey=1"} {
		_, err := second.Execute(ctx, "", source)
		if err == nil {
			t.Fatal("readonly connector accepted an unsupported write")
		}
		receipts := second.(interface{ OriginalQueryReceipts() []agentbridge.Receipt }).OriginalQueryReceipts()
		if len(receipts) != 1 || !receipts[0].ExecutorAcknowledged || receipts[0].RemoteError == nil {
			t.Fatal("connector rejection lost original executor receipt")
		}
	}
	if err := r.Client.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := first.Execute(ctx, "", "INSERT INTO "+table+" VALUES(3,'lost')"); !errors.Is(err, contract.ErrSQLSessionLost) {
		t.Fatal("lost original runtime recovered or replayed implicitly")
	}
	pool.Close()
	_, _, replacement := realTrinoRuntime(t, f)
	_, recovered, _ := realTrinoPool(t, f, replacement.Client)
	if count := realTrinoExecute(t, recovered, "SELECT count(*) FROM "+table).Sets[0].Rows[0][0].Text; count != "1" {
		t.Fatal("explicit recovery replayed SQL or lost confirmed native write")
	}
}

func TestTrinoAgentNativeCancelOriginalExecutorAndQueuedRequest(t *testing.T) {
	f := realTrinoAgentFixture(t)
	_, _, r := realTrinoRuntime(t, f)
	s := realTrinoSession(t, r.Client, f.params(t))
	params := f.params(t)
	params["sessionRole"] = "metadata"
	probe := realTrinoSession(t, r.Client, params)
	marker := "nyadb_agent_cancel_" + strings.ToLower(rand.Text())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type outcome struct {
		r   agentbridge.Receipt
		err error
	}
	completed := make(chan outcome, 1)
	go func() {
		r, err := s.Call(ctx, "execute_query", map[string]any{"sql": "SELECT /* " + marker + " */ sum(sin(extendedprice)) FROM tpch.sf1000.lineitem", "maxRows": 1, "timeoutSecs": 60})
		completed <- outcome{r, err}
	}()
	deadline := time.Now().Add(20 * time.Second)
	var queryID string
	for queryID == "" {
		q := realTrinoQuery(t, probe, "SELECT query_id FROM system.runtime.queries WHERE state='RUNNING' AND query LIKE 'SELECT /* "+marker+" */%'")
		if len(q.SQL.Sets[0].Rows) > 0 {
			queryID = q.SQL.Sets[0].Rows[0][0].Text
			break
		}
		select {
		case <-completed:
			t.Fatal("native executor finished before cancellation observation")
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("original live Trino query was not observed")
		}
		time.Sleep(25 * time.Millisecond)
	}
	queued, stop := context.WithTimeout(context.Background(), 50*time.Millisecond)
	rc, err := s.Call(queued, "execute_query", map[string]any{"sql": "SELECT 77"})
	stop()
	if !errors.Is(err, context.DeadlineExceeded) || rc.Dispatched || rc.CancellationRequested || rc.ExecutorAcknowledged {
		t.Fatal("queued cancellation reached original running executor")
	}
	if state := realTrinoScalar(t, probe, "SELECT state FROM system.runtime.queries WHERE query_id='"+queryID+"'"); state != "RUNNING" {
		t.Fatal("queued canceled request stopped the active original query")
	}
	cancel()
	select {
	case result := <-completed:
		if !errors.Is(result.err, context.Canceled) || !result.r.Dispatched || !result.r.CancellationRequested || !result.r.ExecutorAcknowledged || result.r.ProcessExitConfirmed {
			t.Fatalf("original native cancellation acknowledgment lost: %v", result.err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("original native executor did not acknowledge cancellation")
	}
	if !probe.Alive() || realTrinoScalar(t, probe, "SELECT 1") != "1" {
		t.Fatal("cancel reached an independent original session")
	}
	if state := realTrinoScalar(t, probe, "SELECT state FROM system.runtime.queries WHERE query_id='"+queryID+"'"); state != "FAILED" {
		t.Fatal("driver acknowledgment preceded original server query cancellation")
	}
	t.Log("Observed original server query " + queryID + " running before cancellation and failed after executor acknowledgment")
}

func TestTrinoAgentNativeAuthenticationAndTrust(t *testing.T) {
	f := realTrinoAgentFixture(t)
	if f.TrustFile == "" {
		// Plain transport must still execute against the actual server. Its
		// acceptance is distinct from password and certificate validation.
		_, _, r := realTrinoRuntime(t, f)
		s := realTrinoSession(t, r.Client, f.params(t))
		if realTrinoScalar(t, s, "SELECT current_user") != f.Username {
			t.Fatal("native plaintext user changed")
		}
		t.Log("Plain transport verified; password/certificate checks require the separate TLS run")
		return
	}
	_, _, survivorRuntime := realTrinoRuntime(t, f)
	survivor := realTrinoSession(t, survivorRuntime.Client, f.params(t))
	for _, test := range []string{"wrong-password", "untrusted-certificate"} {
		t.Run(test, func(t *testing.T) {
			_, _, failedRuntime := realTrinoRuntime(t, f)
			params := f.params(t)
			secret := params["password"].(string)
			if test == "wrong-password" {
				params["password"] = secret + "wrong"
			} else {
				endpoint, _ := url.Parse(f.Endpoint)
				params["connection_string"] = "jdbc:trino://" + endpoint.Host + "/memory/default?SSL=true"
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			attempt, receipt, err := failedRuntime.Client.OpenSession(ctx, params)
			if err == nil {
				// The original JDBC open can be lazy. Validate authentication
				// and trust with an actual server request before treating this
				// as a usable connection; an open acknowledgment is insufficient.
				t.Log("Original logical open succeeded; validating with native SELECT 1")
				receipt, err = attempt.Call(ctx, "execute_query", map[string]any{"sql": "SELECT 1", "maxRows": 1, "timeoutSecs": 10})
				defer attempt.Close()
			}
			if err == nil || strings.Contains(err.Error(), secret) || !receipt.Dispatched || !receipt.ExecutorAcknowledged || receipt.RemoteError == nil {
				t.Fatal("native authentication/trust failure receipt missing or credential exposed")
			}
			if receipt.RemoteError.OperationOutcome == "completed" {
				t.Fatal("failed native validation misreported a completed operation")
			}
			if receipt.RemoteError.SessionDisposition == "replace_runtime" && (!receipt.ProcessExitConfirmed || failedRuntime.Client.Alive()) {
				t.Fatal("failed original runtime replacement ignored")
			}
			if realTrinoScalar(t, survivor, "SELECT 1") != "1" {
				t.Fatal("failed original authentication retired an independent runtime")
			}
			t.Logf("Native validation failed with original category=%s, disposition=%s, outcome=%s", receipt.RemoteError.Category, receipt.RemoteError.SessionDisposition, receipt.RemoteError.OperationOutcome)
		})
	}
}

func TestTrinoAgentNativeOriginalProcessFailureKeepsUnknownWriteReceipt(t *testing.T) {
	f := realTrinoAgentFixture(t)
	_, _, original := realTrinoRuntime(t, f)
	_, writer, _ := realTrinoPool(t, f, original.Client)
	_, _, independent := realTrinoRuntime(t, f)
	probe := realTrinoSession(t, independent.Client, f.params(t))
	table := "memory.default.owned_failure_" + strings.ToLower(rand.Text())
	realTrinoExecute(t, writer, "CREATE TABLE "+table+" (id BIGINT)")
	marker := "nyadb_agent_failure_" + strings.ToLower(rand.Text())
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	type outcome struct {
		q   contract.SQLResult
		err error
	}
	completed := make(chan outcome, 1)
	go func() {
		q, err := writer.Execute(ctx, "", "INSERT /* "+marker+" */ INTO "+table+" SELECT orderkey FROM tpch.sf1000.lineitem; INSERT INTO "+table+" VALUES (-1)")
		completed <- outcome{q, err}
	}()
	deadline := time.Now().Add(20 * time.Second)
	var queryID string
	for queryID == "" {
		q := realTrinoQuery(t, probe, "SELECT query_id FROM system.runtime.queries WHERE state='RUNNING' AND query LIKE 'INSERT /* "+marker+" */%'")
		if len(q.SQL.Sets[0].Rows) > 0 {
			queryID = q.SQL.Sets[0].Rows[0][0].Text
			break
		}
		select {
		case <-completed:
			t.Fatal("original write finished before process-failure observation")
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("original server write was not observed running")
		}
		time.Sleep(25 * time.Millisecond)
	}
	// Kill only this captured managed Java process, after inspecting its live
	// original command. The independent runtime keeps the original server
	// query observable and cleans up that exact fixture-owned query below.
	command, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", original.Client.PID()))
	if err != nil || !strings.Contains(string(command), "com.dbx.agent.trino.TrinoAgent") || !strings.Contains(string(command), f.Directory) || !original.Client.Alive() {
		t.Fatal("captured original process identity changed")
	}
	process, err := os.FindProcess(original.Client.PID())
	if err != nil || process.Kill() != nil {
		t.Fatal("captured original process could not be terminated")
	}
	select {
	case result := <-completed:
		if !errors.Is(result.err, contract.ErrSQLSessionLost) || len(result.q.Steps) != 1 || result.q.Steps[0].State != "uncertain" || result.q.Steps[0].AffectedRows != 0 {
			t.Fatal("lost original write was reported complete or suffix was dispatched")
		}
	case <-time.After(20 * time.Second):
		t.Fatal("original process exit did not settle its captured write")
	}
	receipts := writer.(interface{ OriginalQueryReceipts() []agentbridge.Receipt }).OriginalQueryReceipts()
	if len(receipts) != 1 || !receipts[0].Dispatched || receipts[0].ExecutorAcknowledged || !receipts[0].ProcessExitConfirmed || receipts[0].RemoteError != nil {
		t.Fatal("process exit repaired or discarded the unknown original write receipt")
	}
	if !independent.Client.Alive() || realTrinoScalar(t, probe, "SELECT 1") != "1" {
		t.Fatal("original process failure reached an independent runtime")
	}
	// Server cancellation is cleanup, never proof of the missing original
	// write acknowledgment. Keep the original receipt unknown afterwards.
	state := realTrinoScalar(t, probe, "SELECT state FROM system.runtime.queries WHERE query_id='"+queryID+"'")
	if state == "RUNNING" || state == "QUEUED" {
		realTrinoQuery(t, probe, "CALL system.runtime.kill_query(query_id => '"+queryID+"', message => 'owned NyaDB agent failure cleanup')")
	}
	if _, err := writer.Execute(ctx, "", "INSERT INTO "+table+" VALUES(-2)"); !errors.Is(err, contract.ErrSQLSessionLost) {
		t.Fatal("unknown original write allowed implicit recovery or replay")
	}
	if receipts[0].ExecutorAcknowledged || !receipts[0].ProcessExitConfirmed {
		t.Fatal("later server cleanup changed the original unknown receipt")
	}
	t.Log("Original running write " + queryID + " lost its captured executor; receipt remains unknown after independent server cleanup")
}
