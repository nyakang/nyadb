package dbruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/agentbridge"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/databend"
	"github.com/nyakang/nyadb/internal/services/driverstore"
)

func TestDatabendRuntimeOriginalGenerationsIsolationAndLatePublication(t *testing.T) {
	if os.Getenv("NYADB_DATABEND_MANAGED_IT") != "1" {
		t.Skip("requires owned actual Databend and reproducible original managed packages")
	}
	var f struct{ Endpoint, Username, PasswordFile, Archive, SHA256, Version, Directory, Database string }
	raw, err := os.ReadFile(os.Getenv("NYADB_DATABEND_MANAGED_FIXTURE"))
	if err != nil || json.Unmarshal(raw, &f) != nil {
		t.Fatal("original fixture missing", err)
	}
	secret, err := os.ReadFile(f.PasswordFile)
	if err != nil {
		t.Fatal(err)
	}
	store, err := driverstore.New(filepath.Join(f.Directory, t.Name(), "store"))
	if err != nil {
		t.Fatal(err)
	}
	archive, err := os.Open(f.Archive)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.Install(context.Background(), archive, f.SHA256)
	archive.Close()
	if err != nil {
		t.Fatal("original managed package installation", err)
	}
	journal := func(name string, value any) error {
		file, err := os.OpenFile(filepath.Join(f.Directory, name), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		defer file.Close()
		data, err := json.Marshal(value)
		if err == nil {
			_, err = file.Write(append(data, '\n'))
		}
		return err
	}
	provider := databend.Provider{Store: store, Started: func(pid int) error {
		exe, err := os.Readlink(fmt.Sprintf("/proc/%d/exe", pid))
		if err != nil || !strings.HasPrefix(exe, f.Directory+string(os.PathSeparator)) || !strings.HasSuffix(exe, "/jre/bin/java") {
			return errors.New("original installed process executable differs")
		}
		stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
		if err != nil {
			return err
		}
		fields := strings.Fields(string(stat[strings.LastIndexByte(string(stat), ')')+1:]))
		if len(fields) < 20 {
			return errors.New("original process start identity missing")
		}
		return journal("processes.jsonl", map[string]any{"pid": pid, "start_ticks": fields[19], "executable": exe})
	}, Observe: func(method string, r agentbridge.Receipt) {
		bytes := r.OriginalResponse()
		hash := sha256.Sum256(bytes)
		if err := journal("original-receipts.jsonl", map[string]any{"test": t.Name(), "method": "runtime-" + method, "receipt": r, "native_bytes": []byte(bytes), "native_sha256": hex.EncodeToString(hash[:])}); err != nil {
			t.Error("original runtime receipt recording", err)
		}
	}}
	u, err := url.Parse(f.Endpoint)
	if err != nil {
		t.Fatal(err)
	}
	host, port, err := net.SplitHostPort(u.Host)
	if err != nil {
		t.Fatal(err)
	}
	portNumber, err := strconv.ParseUint(port, 10, 16)
	if err != nil {
		t.Fatal(err)
	}
	p, _ := connections.Defaults(connections.Databend)
	p.ID, p.Name, p.Host, p.Port, p.Username, p.Database = 601, "original runtime", host, uint16(portNumber), f.Username, f.Database
	p.DatabendOptions.DriverVersion, p.DatabendOptions.DriverSHA256 = f.Version, f.SHA256
	p.DatabendOptions.ConnectTimeoutSeconds = 60
	m := New()
	defer m.Close()
	// Registration is private to this manager; builtins remain unpublished.
	m.Register(connections.Databend, provider)
	connect := func(profile connections.Profile) contract.Session {
		t.Helper()
		s, err := m.ConnectSession(context.Background(), profile, string(secret))
		if err != nil {
			t.Fatal("actual Databend runtime connect", err)
		}
		return s
	}
	reserve := func(s contract.Session) contract.QueryLease {
		t.Helper()
		l, err := s.(contract.QueryProvider).ReserveQuery(1)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(l.Release)
		if err := l.Preconnect(context.Background()); err != nil {
			t.Fatal(err)
		}
		return l
	}
	query := func(l contract.QueryLease, schema, source string) contract.SQLResult {
		t.Helper()
		r, err := l.Execute(context.Background(), schema, source)
		if err != nil {
			t.Fatal("original runtime query", err)
		}
		return r
	}
	original := connect(p)
	first := reserve(original)
	write := query(first, f.Database, "INSERT INTO managed_writes VALUES (55)")
	if write.AffectedRows != 1 || len(write.Steps) != 1 || write.Steps[0].State != "completed" {
		t.Fatal("original runtime write acknowledgement missing")
	}
	otherProfile := p
	otherProfile.ID = 602
	otherProfile.Database = strings.Replace(f.Database, "nyadb_transport_", "nyadbXtransport_", 1)
	other := connect(otherProfile)
	otherLease := reserve(other)
	oldGeneration := original.Generation()
	process, err := os.FindProcess(original.(*session).raw.(*databend.Session).PID())
	if err != nil || process.Kill() != nil {
		t.Fatal("original process fault injection failed")
	}
	deadline := time.Now().Add(5 * time.Second)
	for m.IsConnected(p.ID) && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if m.IsConnected(p.ID) || m.Generation(p.ID) != 0 {
		t.Fatal("dead original publication remained usable")
	}
	if _, err := first.Execute(context.Background(), f.Database, "INSERT INTO managed_writes VALUES (56)"); err == nil {
		t.Fatal("dead original lease executed or recovered implicitly")
	}
	if q := query(otherLease, otherProfile.Database, "SELECT current_database()"); q.Sets[0].Rows[0][0].Text != otherProfile.Database || m.Generation(otherProfile.ID) != other.Generation() {
		t.Fatal("failed original process retired independent profile")
	}
	replacement := connect(p)
	if replacement.Generation() <= oldGeneration {
		t.Fatal("explicit original-profile recovery did not publish a new generation")
	}
	if _, err := first.Replace(); err == nil {
		t.Fatal("old lease recovered against replacement publication")
	}
	_ = original.Close()
	if !m.IsConnected(p.ID) {
		t.Fatal("late original close retired replacement publication")
	}
	recovered := reserve(replacement)
	if q := query(recovered, f.Database, "SELECT id FROM managed_writes WHERE id IN (55,56) ORDER BY id"); len(q.Sets[0].Rows) != 1 || q.Sets[0].Rows[0][0].Text != "55" {
		t.Fatal("explicit generation recovery replayed or lost original write")
	}
	lateProfile := p
	lateProfile.ID = 603
	ready, finish := make(chan contract.Session, 1), make(chan struct{})
	defer close(finish)
	var firstOpen atomic.Bool
	m.Register(connections.Databend, ProviderFunc(func(ctx context.Context, p connections.Profile, secret string) (contract.Session, error) {
		s, err := provider.Open(ctx, p, secret)
		if err == nil && p.ID == lateProfile.ID && firstOpen.CompareAndSwap(false, true) {
			ready <- s
			<-finish
		}
		return s, err
	}))
	done := make(chan error, 1)
	go func() {
		_, err := m.ConnectSession(context.Background(), lateProfile, string(secret))
		done <- err
	}()
	var late contract.Session
	select {
	case late = <-ready:
	case <-time.After(45 * time.Second):
		t.Fatal("original ready handle did not reach captured publication gate")
	}
	newer := connect(lateProfile)
	finish <- struct{}{}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("late original handle was published over replacement")
		}
	case <-time.After(30 * time.Second):
		t.Fatal("late original handle cleanup did not settle")
	}
	if late.(*databend.Session).Alive() || m.Generation(lateProfile.ID) != newer.Generation() {
		t.Fatal("late original cleanup leaked its process or retired replacement")
	}
	query(reserve(newer), f.Database, "SELECT current_database()")
	if err := journal("runtime-generations.jsonl", map[string]any{"test": t.Name(), "original": oldGeneration, "replacement": replacement.Generation(), "independent": other.Generation(), "late_replacement": newer.Generation(), "first_write": 55, "unsent_write": 56, "recovery_replayed": false}); err != nil {
		t.Fatal(err)
	}
}
