package prestosql

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/jdbcplugin"
)

// Independent HTTP observations identify an actually running original query.
// They do not substitute for the original JDBC executor's acknowledgement.
func providerHTTP(t *testing.T, endpoint, sql string) [][]json.RawMessage {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+"/v1/statement", strings.NewReader(sql))
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	var rows [][]json.RawMessage
	for range 100 {
		req.Header.Set("X-Presto-User", "nyadb_provider_observer")
		response, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
		response.Body.Close()
		if err != nil || response.StatusCode != 200 {
			t.Fatal("original observation failed", err, response.StatusCode)
		}
		var result struct {
			Data  [][]json.RawMessage `json:"data"`
			Next  string              `json:"nextUri"`
			Error json.RawMessage     `json:"error"`
		}
		if json.Unmarshal(raw, &result) != nil || len(result.Error) > 0 {
			t.Fatal("original observation query failed")
		}
		rows = append(rows, result.Data...)
		if result.Next == "" {
			return rows
		}
		next, e := url.Parse(result.Next)
		origin, _ := url.Parse(endpoint)
		if e != nil || next.Scheme != origin.Scheme || next.Host != origin.Host {
			t.Fatal("observation escaped owned endpoint")
		}
		req, err = http.NewRequestWithContext(ctx, http.MethodGet, result.Next, nil)
		if err != nil {
			t.Fatal(err)
		}
	}
	t.Fatal("original observation exceeded page bound")
	return nil
}

func providerRunning(t *testing.T, endpoint, sql string) {
	t.Helper()
	deadline := time.Now().Add(40 * time.Second)
	for time.Now().Before(deadline) {
		for _, row := range providerHTTP(t, endpoint, "SELECT state FROM system.runtime.queries WHERE query='"+strings.ReplaceAll(sql, "'", "''")+"'") {
			if len(row) == 1 && string(row[0]) == `"RUNNING"` {
				return
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("original query never reached RUNNING")
}

func providerProof(t *testing.T, f providerFixture, name string, value any) {
	t.Helper()
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(f.Directory, name), raw, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestPrestoSQLPrivateProviderNativeActiveCancellationReleaseAndClose(t *testing.T) {
	f, p, c, e := nativeProvider(t)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	s, err := p.OpenManaged(ctx, 64, c, Credentials{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	var proofs []map[string]any
	for i, mode := range []string{"cancel", "release", "close"} {
		q, err := s.ReserveQuery(uint64(i + 1))
		if err != nil {
			t.Fatal(err)
		}
		if err = q.Preconnect(ctx); err != nil {
			t.Fatal(err)
		}
		original := q.(*queryLease)
		sql := "SELECT sum(a.orderkey * b.orderkey) FROM tpch.sf100.lineitem a CROSS JOIN tpch.sf100.lineitem b /* provider64 " + mode + " */"
		active, stop := context.WithCancel(ctx)
		type outcome struct {
			result contract.SQLResult
			err    error
		}
		done := make(chan outcome, 1)
		go func() { r, err := q.Execute(active, "", sql); done <- outcome{r, err} }()
		providerRunning(t, f.Endpoint, sql)
		settled := make(chan error, 1)
		switch mode {
		case "cancel":
			stop()
		case "release":
			go func() { q.Release(); settled <- nil }()
		case "close":
			go func() { settled <- s.Close() }()
		}
		var result outcome
		select {
		case result = <-done:
		case <-time.After(30 * time.Second):
			stop()
			t.Fatal("original active executor failed to settle")
		}
		stop()
		r := e.originalFailure(original.role)
		if result.err == nil || len(result.result.Steps) != 1 || !r.Dispatched || !r.CancellationRequested || !r.ExecutorAcknowledged || len(r.CancellationResponse()) == 0 {
			t.Fatal("original cancellation acknowledgement missing", result.err, r)
		}
		if mode != "cancel" {
			select {
			case err = <-settled:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(30 * time.Second):
				t.Fatal("owner close failed to settle")
			}
			if _, err = q.Execute(ctx, "", "SELECT 99"); !errors.Is(err, contract.ErrRevoked) {
				t.Fatal("closed owner accepted SQL", err)
			}
		} else {
			// Native typed diagnostics determine whether this owner is reusable.
			// If it was quarantined, only an explicit replacement may reconnect.
			if errors.Is(original.CheckQueryLease(), contract.ErrSQLSessionLost) {
				if err = q.Preconnect(ctx); !errors.Is(err, contract.ErrSQLSessionLost) {
					t.Fatal("canceled lost owner recovered implicitly", err)
				}
				q, err = q.Replace()
				if err != nil {
					t.Fatal(err)
				}
			}
			queryNative(t, q, "", "SELECT 66")
			q.Release()
		}
		proofs = append(proofs, map[string]any{"mode": mode, "observed_running": true, "outcome": result.result, "receipt": r, "native_bytes": []byte(r.OriginalResponse()), "cancellation_bytes": []byte(r.CancellationResponse())})
	}
	if err = p.Store.Remove(ctx, "prestosql", c.DriverVersion); err != nil {
		t.Fatal(err)
	}
	providerProof(t, f, "provider-active-lifecycle.json", proofs)
}

func TestPrestoSQLPrivateProviderNativeLatePreconnectRevoked(t *testing.T) {
	f, p, c, e := nativeProvider(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	started, proceed := make(chan struct{}), make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(proceed) }) })
	prior := p.Started
	launches := 0
	p.Started = func(pid int) error {
		if err := prior(pid); err != nil {
			return err
		}
		launches++
		if launches == 2 {
			close(started)
			select {
			case <-proceed:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return nil
	}
	s, err := p.OpenManaged(ctx, 64, c, Credentials{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { once.Do(func() { close(proceed) }); _ = s.Close() })
	q, err := s.ReserveQuery(1)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- q.Preconnect(ctx) }()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("original query JVM never started")
	}
	closed := make(chan error, 1)
	go func() { closed <- s.Close() }()
	select {
	case <-s.ctx.Done():
	case <-ctx.Done():
		t.Fatal("session revocation did not begin")
	}
	once.Do(func() { close(proceed) })
	select {
	case err = <-done:
		if err == nil {
			t.Fatal("late query open published into revoked session")
		}
	case <-ctx.Done():
		t.Fatal("late original open did not settle")
	}
	select {
	case err = <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("revoked original session did not close")
	}
	if _, err = q.Execute(ctx, "", "SELECT 99"); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("late revoked owner executed", err)
	}
	if err = p.Store.Remove(ctx, "prestosql", c.DriverVersion); err != nil {
		t.Fatal(err)
	}
	e.mu.Lock()
	count := len(e.processes)
	e.mu.Unlock()
	if count != 2 {
		t.Fatal("late original opening changed process count", count)
	}
	providerProof(t, f, "provider-late-preconnect.json", map[string]any{"actual_query_jvm_started": true, "closed_before_handshake_completed": true, "late_publication_refused": true, "processes": count, "closed_package_removed": true})
}

func ownedProviderDocker(t *testing.T, f providerFixture, args ...string) []byte {
	t.Helper()
	if len(f.ContainerID) != 64 || f.Nonce == "" {
		t.Fatal("explicit owned restart fixture required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	raw, err := exec.CommandContext(ctx, "docker", "inspect", f.ContainerID).Output()
	if err != nil {
		t.Fatal(err)
	}
	var rows []struct {
		ID              string `json:"Id"`
		Config          struct{ Labels map[string]string }
		NetworkSettings struct {
			Ports map[string][]struct{ HostIP, HostPort string }
		}
	}
	if json.Unmarshal(raw, &rows) != nil || len(rows) != 1 || rows[0].ID != f.ContainerID || rows[0].Config.Labels["nyadb.prestosql.research"] != f.Nonce {
		t.Fatal("refuse foreign container action")
	}
	if len(args) == 0 {
		return raw
	}
	out, err := exec.CommandContext(ctx, "docker", append(args, f.ContainerID)...).CombinedOutput()
	if err != nil {
		t.Fatal("owned docker action", err, string(out))
	}
	return out
}

func TestPrestoSQLPrivateProviderNativeActualServerRestart(t *testing.T) {
	f, p, c, e := nativeProvider(t)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	before := ownedProviderDocker(t, f)
	s, err := p.OpenManaged(ctx, 64, c, Credentials{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	q, err := s.ReserveQuery(1)
	if err != nil {
		t.Fatal(err)
	}
	queryNative(t, q, "", "SELECT 67")
	ownedProviderDocker(t, f, "stop", "-t", "10")
	out, err := q.Execute(ctx, "", "SELECT 68")
	if !errors.Is(err, contract.ErrSQLSessionLost) || len(out.Steps) != 1 || out.Steps[0].State != "unknown" {
		t.Fatal("actual stopped server did not quarantine query", err, out)
	}
	receipt := e.originalFailure("query-1")
	if !receipt.Dispatched || !receipt.ProcessExitConfirmed {
		t.Fatal("original stopped-server executor not reaped", receipt)
	}
	if _, err = s.Namespaces(ctx); !errors.Is(err, contract.ErrSQLSessionLost) {
		t.Fatal("stopped server did not revoke metadata session", err)
	}
	if err = s.Close(); err != nil && !errors.Is(err, jdbcplugin.ErrLost) {
		t.Fatal(err)
	}
	ownedProviderDocker(t, f, "start")
	deadline := time.Now().Add(150 * time.Second)
	ready := false
	client := &http.Client{Timeout: 3 * time.Second}
	for time.Now().Before(deadline) {
		response, err := client.Get(f.Endpoint + "/v1/info")
		if err == nil {
			var info struct {
				Starting    bool
				NodeVersion struct{ Version string }
			}
			err = json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&info)
			response.Body.Close()
			if err == nil && response.StatusCode == 200 && !info.Starting && info.NodeVersion.Version == "350" {
				ready = true
				break
			}
		}
		time.Sleep(time.Second)
	}
	if !ready {
		t.Fatal("owned original server did not restart on explicit bound endpoint")
	}
	after := ownedProviderDocker(t, f)
	if _, err = q.Execute(ctx, "", "SELECT 99"); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("old query revived after actual restart", err)
	}
	if _, err = s.ReserveQuery(2); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("old session revived after actual restart", err)
	}
	recovered, err := p.OpenManaged(ctx, 64, c, Credentials{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = recovered.Close() })
	fresh, err := recovered.ReserveQuery(1)
	if err != nil {
		t.Fatal(err)
	}
	read := queryNative(t, fresh, "", "SELECT 69")
	if read.Sets[0].Rows[0][0].Text != "69" {
		t.Fatal("explicit server restart recovery failed")
	}
	if err = recovered.Close(); err != nil {
		t.Fatal(err)
	}
	if err = p.Store.Remove(ctx, "prestosql", c.DriverVersion); err != nil {
		t.Fatal(err)
	}
	providerProof(t, f, "provider-server-restart.json", map[string]any{"before": json.RawMessage(before), "after": json.RawMessage(after), "failed_outcome": out, "original_receipt": receipt, "original_native_bytes": []byte(receipt.OriginalResponse()), "explicit_recovery": read, "old_query_and_session_refused": true, "closed_package_removed": true})
}
