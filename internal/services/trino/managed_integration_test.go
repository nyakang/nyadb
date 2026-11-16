package trino

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/agentbridge"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/driverstore"
)

func managedNativeProvider(t *testing.T) (connections.Profile, string, Provider) {
	t.Helper()
	if os.Getenv("NYADB_TRINO_MANAGED_IT") != "1" {
		t.Skip("requires the owned managed Trino workbench fixture")
	}
	var f struct {
		Archive, SHA256, Directory, Endpoint, Username string
		DriverVersion                                  string `json:"driver_version"`
		PasswordFile                                   string `json:"password_file"`
		CAFile                                         string `json:"ca_file"`
	}
	raw, err := os.ReadFile(os.Getenv("NYADB_AGENT_TRINO_FIXTURE_FILE"))
	if err != nil || json.Unmarshal(raw, &f) != nil {
		t.Fatal("private managed Trino fixture unavailable")
	}
	u, _ := url.Parse(f.Endpoint)
	port, _ := strconv.ParseUint(u.Port(), 10, 16)
	p, _ := connections.Defaults(connections.Trino)
	p.ID, p.Name, p.Host, p.Port, p.Username, p.Database = 301, "original JDBC", u.Hostname(), uint16(port), f.Username, "memory"
	a := &p.TrinoOptions
	a.Mode, a.DefaultSchema, a.CAFile = "jdbc", "default", f.CAFile
	a.DriverVersion, a.DriverSHA256 = f.DriverVersion, f.SHA256
	a.DriverDirectory = filepath.Join(f.Directory, "provider-"+rand.Text())
	a.ConnectTimeoutSeconds, a.QueryTimeoutSeconds, a.MaxConnections, a.MetadataReserve, a.SQLMaxConnections = 120, 30, 4, 1, 2
	if u.Scheme == "http" {
		p.TLSMode = "disable"
	}
	store, err := driverstore.New(a.DriverDirectory)
	if err != nil {
		t.Fatal(err)
	}
	input, err := os.Open(f.Archive)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.Install(context.Background(), input, f.SHA256)
	input.Close()
	if err != nil {
		t.Fatal(err)
	}
	secret, err := os.ReadFile(f.PasswordFile)
	if err != nil {
		t.Fatal("private credential unavailable")
	}
	observe := func(pid int) error {
		stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
		if err != nil {
			return err
		}
		fields := strings.Fields(string(stat[strings.LastIndexByte(string(stat), ')')+1:]))
		command, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
		if err != nil {
			return err
		}
		args := strings.Split(string(command), "\x00")
		var agent string
		for i, arg := range args {
			if arg == "-cp" && i+1 < len(args) {
				agent = strings.Split(args[i+1], string(os.PathListSeparator))[0]
			}
		}
		if len(fields) < 20 || agent == "" {
			return errors.New("original process identity unavailable")
		}
		record, _ := json.Marshal(map[string]any{"pid": pid, "start_ticks": fields[19], "agent": agent})
		journal, err := os.OpenFile(filepath.Join(f.Directory, "processes.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		defer journal.Close()
		_, err = journal.Write(append(record, '\n'))
		return err
	}
	return p, string(secret), Provider{Store: store, Started: observe}
}

func TestTrinoManagedNativeProviderQualifiedReadsOriginalValuesAndSQL(t *testing.T) {
	p, secret, provider := managedNativeProvider(t)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	raw, err := provider.Open(ctx, p, secret)
	if err != nil {
		t.Fatal(err)
	}
	s := raw.(*Session)
	defer s.Close()
	names, err := s.Namespaces(ctx)
	if err != nil || len(names) < 3 {
		t.Fatal("qualified managed metadata missing", err)
	}
	label := namespaceLabel("memory", "default")
	if names[0].Label != label || s.Namespace(namespaceLabel("memory", "information_schema")) == s.Namespace(namespaceLabel("system", "information_schema")) {
		t.Fatal("catalog/schema identity collided")
	}
	first, err := s.ReserveQuery(1)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Release()
	second, err := s.ReserveQuery(2)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Release()
	if _, err = s.ReserveQuery(3); !errors.Is(err, contract.ErrSQLSessionLimit) {
		t.Fatal("SQL document admission exceeded configured capacity")
	}
	run := func(l contract.QueryLease, namespace, source string) contract.SQLResult {
		t.Helper()
		q, err := l.Execute(ctx, namespace, source)
		if err != nil || q.NativeJSON == "" {
			t.Fatal("original managed SQL failed", err)
		}
		return q
	}
	table := "owned_managed_" + strings.ToLower(rand.Text())
	qualified := "memory.default." + table
	run(first, label, "CREATE TABLE "+qualified+"(id BIGINT,note VARCHAR,amount DECIMAL(38,20),payload VARBINARY,items ARRAY(BIGINT),stamp TIMESTAMP(12))")
	run(first, label, "INSERT INTO "+qualified+" VALUES(9007199254740993,'中文',DECIMAL '123456789012345678.12345678901234567890',X'00ff',ARRAY[9007199254740993],TIMESTAMP '2026-11-16 01:02:03.123456789123'),(2,'',NULL,NULL,NULL,NULL),(3,'third',NULL,NULL,NULL,NULL)")
	objects, err := s.Browse(ctx, s.Namespace(label))
	if err != nil {
		t.Fatal(err)
	}
	var object contract.ObjectRef
	for _, o := range objects {
		if o.Name == table {
			object = o.Ref
		}
	}
	metadata, err := s.TableMetadata(ctx, object)
	if err != nil || metadata == nil || len(metadata.Columns) != 6 || metadata.Editable || !strings.Contains(metadata.Definition, table) {
		t.Fatal("original qualified definition missing", err)
	}
	page, err := s.DataReads().AdvancedLoader(ctx, p.ID, label, table, contract.DataOptions{Object: object, Limit: 2, SortColumn: "id", SortDescending: true})
	if err != nil || page == nil || len(page.Rows) != 2 || !page.HasMore || page.Editable == nil || *page.Editable {
		t.Fatal("managed readonly common-grid page failed", err)
	}
	row := page.Rows[0]
	if row[0].Value.Kind != contract.Integer || row[0].Value.Text != "9007199254740993" || row[1].Text != "中文" || row[2].Value.Text != "123456789012345678.12345678901234567890" {
		t.Fatal("JDBC scalar grid values changed")
	}
	for _, i := range []int{3, 4, 5} {
		if !row[i].Truncated || row[i].Value == nil || !row[i].Value.Truncated {
			t.Fatal("lossy JDBC value became complete grid data")
		}
	}
	count, err := s.DataReads().Count(ctx, p.ID, label, table)
	if err != nil || count != 3 {
		t.Fatal("managed original count failed", err)
	}
	var values [][]contract.DataValue
	err = s.DataExports().Exporter(ctx, p.ID, label, table, contract.DataOptions{Object: object, SortColumn: "id"}, []string{"id", "note", "amount"}, func(v []contract.DataValue) error { values = append(values, v); return nil })
	if err != nil || len(values) != 3 || values[2][0].Text != "9007199254740993" {
		t.Fatal("complete selected-field export failed", err)
	}
	err = s.DataExports().Exporter(ctx, p.ID, label, table, contract.DataOptions{Object: object}, []string{"payload"}, func(v []contract.DataValue) error { return nil })
	if !errors.Is(err, ErrIncompleteJDBC) {
		t.Fatal("incomplete JDBC binary was exported as complete data", err)
	}
	if _, err = s.DataReads().AdvancedLoader(ctx, p.ID, label, table, contract.DataOptions{Object: object, Limit: 2, UseKeyset: true}); !errors.Is(err, contract.ErrUnsupported) {
		t.Fatal("unverified keyset identity was admitted")
	}
	if !s.Capabilities().EditableTables || contract.ComposeData(s).ReserveDocument == nil || contract.ComposeData(s).Saver != nil {
		t.Fatal("managed INSERT must require an original document plan")
	}
	run(first, "", "USE tpch.tiny; PREPARE captured FROM SELECT current_catalog,current_schema")
	q := run(first, "", "EXECUTE captured")
	if q.Sets[0].Rows[0][0].Text != "tpch" || q.Sets[0].Rows[0][1].Text != "tiny" {
		t.Fatal("captured physical affinity lost")
	}
	q = run(second, label, "SELECT current_catalog,current_schema")
	if q.Sets[0].Rows[0][0].Text != "memory" || q.Sets[0].Rows[0][1].Text != "default" {
		t.Fatal("state leaked to another document")
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-s.managed.runtime.Client.Exited():
	default:
		t.Fatal("close returned before original agent process exited")
	}
	if _, err = first.Execute(ctx, "", "INSERT INTO "+qualified+" VALUES(4,'lost',NULL,NULL,NULL,NULL)"); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("closed lease recovered implicitly", err)
	}
	if err = provider.Store.Remove(ctx, "trino", p.TrinoOptions.DriverVersion); err != nil {
		t.Fatal("original runtime retained package after terminal cleanup", err)
	}
}

func TestTrinoManagedNativePoolReserveCancellationAndOriginalReceipts(t *testing.T) {
	p, secret, provider := managedNativeProvider(t)
	p.TrinoOptions.MaxConnections, p.TrinoOptions.MetadataReserve, p.TrinoOptions.SQLMaxConnections = 2, 1, 1
	// Cold native driver allocation uses the normal pool deadline. The
	// queued cancellation below has its own short request deadline.
	p.TrinoOptions.PoolWaitTimeoutMilliseconds = 30000
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	raw, err := provider.Open(ctx, p, secret)
	if err != nil {
		t.Fatal(err)
	}
	s := raw.(*Session)
	defer s.Close()
	lease, err := s.ReserveQuery(1)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	if err = lease.Preconnect(ctx); err != nil {
		t.Fatal(err)
	}
	// Use the native workload admission directly: the SQL lease owns the sole
	// workload slot, while metadata remains available on its reserved slot.
	queued, stop := context.WithTimeout(ctx, 50*time.Millisecond)
	_, err = s.managed.acquire(queued, false)
	stop()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("canceled capacity waiter dispatched work", err)
	}
	if _, err = s.Namespaces(ctx); err != nil {
		t.Fatal("SQL saturation consumed metadata reserve", err)
	}
	q, err := lease.Execute(ctx, "", "SELECT 9007199254740993; SELECT no_such_original_column; SELECT 77")
	if err == nil || len(q.Steps) != 2 || q.Steps[0].State != "completed" || q.NativeJSON == "" {
		t.Fatal("original sequential receipts/suffix boundary lost")
	}
	receipts := lease.(interface{ OriginalQueryReceipts() []agentbridge.Receipt }).OriginalQueryReceipts()
	if len(receipts) != 2 || !receipts[0].ExecutorAcknowledged || receipts[1].RemoteError == nil {
		t.Fatal("original native execution receipts missing")
	}
	original := bytes.Clone(receipts[0].Result)
	receipts[0].Result[0] = ' '
	if !bytes.Equal(lease.(interface{ OriginalQueryReceipts() []agentbridge.Receipt }).OriginalQueryReceipts()[0].Result, original) {
		t.Fatal("receipt snapshot was mutable")
	}
}

func TestTrinoManagedNativeAuthenticationAndExplicitRecovery(t *testing.T) {
	p, secret, provider := managedNativeProvider(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	raw, err := provider.Open(ctx, p, secret)
	if err != nil {
		t.Fatal(err)
	}
	s := raw.(*Session)
	defer s.Close()
	lease, err := s.ReserveQuery(1)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	table := "memory.default.owned_recovery_" + strings.ToLower(rand.Text())
	if _, err = lease.Execute(ctx, "", "CREATE TABLE "+table+"(id BIGINT); INSERT INTO "+table+" VALUES(1)"); err != nil {
		t.Fatal(err)
	}
	if secret != "" {
		for _, name := range []string{"wrong-password", "untrusted-certificate", "wrong-hostname"} {
			t.Run(name, func(t *testing.T) {
				bad, credential := p, secret
				switch name {
				case "wrong-password":
					credential += "wrong"
				case "untrusted-certificate":
					bad.TrinoOptions.CAFile = ""
				case "wrong-hostname":
					bad.TrinoOptions.ServerName = "wrong-host.invalid"
				}
				if wrong, err := provider.Open(ctx, bad, credential); err == nil {
					wrong.Close()
					t.Fatal("lazy JDBC logical open bypassed actual authenticated/trusted query")
				}
			})
		}
	}
	if _, err = lease.Execute(ctx, "", "SELECT 1"); err != nil {
		t.Fatal("failed connection validation reached original session", err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = lease.Execute(ctx, "", "INSERT INTO "+table+" VALUES(2)"); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("disconnected original document implicitly replayed")
	}
	recovered, err := provider.Open(ctx, p, secret)
	if err != nil {
		t.Fatal(err)
	}
	defer recovered.Close()
	newLease, err := recovered.(contract.QueryProvider).ReserveQuery(2)
	if err != nil {
		t.Fatal(err)
	}
	defer newLease.Release()
	q, err := newLease.Execute(ctx, "", "SELECT count(*) FROM "+table)
	if err != nil || q.Sets[0].Rows[0][0].Text != "1" {
		t.Fatal("explicit original-profile recovery replayed SQL", err)
	}
}
