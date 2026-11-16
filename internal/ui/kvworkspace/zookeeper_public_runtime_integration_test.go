package kvworkspace

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/dbruntime"
	"github.com/nyakang/nyadb/internal/services/driverstore"
	"github.com/nyakang/nyadb/internal/services/zookeeper"
	"github.com/nyakang/nyadb/internal/storage/localdb"
)

// Observe successful builtin launches independently, without replacing the
// builtin registration or its provider. Each fixture owns one installed path.
func nativeZooBuiltinProcesses(t *testing.T, f nativeZooFixture) map[int]bool {
	t.Helper()
	executable := filepath.Join(f.options.Driver.Directory, "zookeeper@"+f.options.Driver.Version, "bin", "agent")
	entries, err := os.ReadDir("/proc")
	if err != nil {
		t.Fatal(err)
	}
	pids := map[int]bool{}
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		actual, err := os.Readlink(filepath.Join("/proc", entry.Name(), "exe"))
		if err == nil && actual == executable {
			pids[pid] = true
		}
	}
	return pids
}

func nativeZooObserveBuiltinLaunch(t *testing.T, f nativeZooFixture, before map[int]bool) {
	t.Helper()
	found := 0
	for pid := range nativeZooBuiltinProcesses(t, f) {
		if !before[pid] {
			if err := f.options.Started(pid); err != nil {
				t.Fatal("could not journal the original builtin process", err)
			}
			found++
		}
	}
	if found != 1 {
		t.Fatal("builtin did not start one independent original process", found)
	}
}

func TestZooKeeperNativePublicRuntimeURIStoragePinnedDriverIsolationAndRecovery(t *testing.T) {
	if os.Getenv("NYADB_ZOOKEEPER_PUBLIC_RUNTIME_IT") != "1" {
		t.Skip("requires explicit public runtime acceptance with an owned official ZooKeeper")
	}
	f := nativeZooSetup(t)
	bootstrap := nativeZooSession(t, f, zookeeper.Config{})
	observer := nativeZooLease(t, bootstrap, 1)
	root := "/nyadb_public_" + rand.Text()
	nativeZooCall(t, observer, "kv_put", nativeZooPut(root, nil, "persistent"))
	nativeZooCall(t, observer, "kv_put", nativeZooPut(root+"/ns", nil, "persistent"))
	t.Cleanup(func() { nativeZooCall(t, observer, "kv_delete", map[string]any{"key": root, "recursive": true}) })
	q := url.Values{
		"name": {"public original ZooKeeper"}, "namespace": {"/ns"}, "prefix": {"/suggestion"},
		"driver_version": {f.options.Driver.Version}, "driver_sha256": {f.options.Driver.SHA256}, "driver_directory": {f.options.Driver.Directory},
		"session_timeout": {"2000"}, "connect_timeout": {"5000"}, "max_documents": {"2"},
	}
	parsed, err := connections.ParseURI("zookeeper://" + f.Endpoint + root + "?" + q.Encode())
	if err != nil {
		t.Fatal("public URI rejected original managed profile", err)
	}
	path := filepath.Join(t.TempDir(), "config.sqlite")
	db, err := localdb.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	profiles := connections.NewService(localdb.NewProfileStore(db))
	p, err := profiles.Create(context.Background(), parsed.Profile)
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = localdb.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	restored, err := connections.NewService(localdb.NewProfileStore(db)).Get(context.Background(), p.ID)
	if err != nil || restored.ZooKeeperOptions != p.ZooKeeperOptions || restored.Host != p.Host || restored.Port != p.Port {
		t.Fatal("restart changed original public profile", err)
	}
	m := dbruntime.New()
	dbruntime.RegisterBuiltins(m, nil)
	t.Cleanup(func() { m.Close() })
	before := nativeZooBuiltinProcesses(t, f)
	session, err := m.ConnectSession(context.Background(), restored, parsed.Password)
	if err != nil {
		t.Fatal("real builtin provider failed", err)
	}
	nativeZooObserveBuiltinLaunch(t, f, before)
	if session.Capabilities() != (contract.Capabilities{KeyValues: true, Export: true}) {
		t.Fatal("ZooKeeper native capability publication changed", session.Capabilities())
	}
	provider, err := m.KVRequests(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	scope := provider.KVScope()
	if scope.Generation != m.Generation(p.ID) || scope.Namespace.ProfileID != p.ID || scope.ZNode.Root != root+"/ns" || scope.ZNode.DriverVersion != p.ZooKeeperOptions.DriverVersion || scope.ZNode.DriverSHA256 != p.ZooKeeperOptions.DriverSHA256 || scope.Version == "" {
		t.Fatal("builtin lost original namespace, package or server version")
	}
	if metadata, err := contract.Require[contract.MetadataProvider](session); err == nil {
		if _, err := metadata.Namespaces(context.Background()); !errors.Is(err, contract.ErrUnsupported) {
			t.Fatal("ZooKeeper published relational metadata")
		}
	}
	if query, err := contract.Require[contract.QueryProvider](session); err == nil {
		if _, err := query.ReserveQuery(1); !errors.Is(err, contract.ErrUnsupported) {
			t.Fatal("ZooKeeper published SQL execution")
		}
	}
	first, err := provider.ReserveKV(1)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { first.Close() })
	second, err := provider.ReserveKV(2)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { second.Close() })
	if extra, err := provider.ReserveKV(3); err == nil {
		extra.Close()
		t.Fatal("saved document capacity was ignored")
	}
	before = nativeZooBuiltinProcesses(t, f)
	prepared := nativeZooPlan(t, first, "kv_put", nativeZooPut("/logical", []byte{0, 255, 128}, "persistent"))
	if _, err := second.Execute(context.Background(), prepared); !errors.Is(err, contract.ErrKVPlanUsed) {
		t.Fatal("public request transferred to another document")
	}
	if got := nativeZooBuiltinProcesses(t, f); len(got) != len(before) {
		t.Fatal("reservation or preparation opened another physical session")
	}
	result, err := first.Execute(context.Background(), prepared)
	if err != nil || !result.Completed || result.ZNode == nil || !result.ZNode.Acknowledged {
		t.Fatal("public write did not retain original receipt", err)
	}
	nativeZooObserveBuiltinLaunch(t, f, before)
	if _, err := first.Execute(context.Background(), prepared); !errors.Is(err, contract.ErrKVPlanUsed) {
		t.Fatal("original public write replayed")
	}
	if node := nativeZooCall(t, observer, "kv_get", map[string]string{"key": root + "/ns/logical"}).ZNode.Reply.Node; node == nil || !bytes.Equal(node.Value, []byte{0, 255, 128}) {
		t.Fatal("logical absolute key or bytes changed at the real server")
	}
	before = nativeZooBuiltinProcesses(t, f)
	nativeZooCall(t, first, "kv_put", nativeZooPut("/session-a", nil, "ephemeral"))
	nativeZooCall(t, second, "kv_put", nativeZooPut("/session-b", nil, "ephemeral"))
	nativeZooObserveBuiltinLaunch(t, f, before)
	a := nativeZooCall(t, observer, "kv_get", map[string]string{"key": root + "/ns/session-a"}).ZNode.Reply.Node
	b := nativeZooCall(t, observer, "kv_get", map[string]string{"key": root + "/ns/session-b"}).ZNode.Reply.Node
	if a.Stat.EphemeralOwner == 0 || b.Stat.EphemeralOwner == 0 || a.Stat.EphemeralOwner == b.Stat.EphemeralOwner {
		t.Fatal("builtin documents share a real ZooKeeper session")
	}
	store, err := driverstore.New(f.options.Driver.Directory)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Remove(context.Background(), "zookeeper", f.options.Driver.Version); err == nil {
		t.Fatal("leased original package could be removed")
	}
	if err := m.Disconnect(p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := first.Prepare(contract.KVRequest{Method: "kv_get", JSON: `{"key":"/logical"}`}); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("disconnected original lease remained executable")
	}
	before = nativeZooBuiltinProcesses(t, f)
	if _, err := m.ConnectSession(context.Background(), restored, ""); err != nil {
		t.Fatal(err)
	}
	nativeZooObserveBuiltinLaunch(t, f, before)
	provider, err = m.KVRequests(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	before = nativeZooBuiltinProcesses(t, f)
	recovery, err := provider.RecoverKV(first)
	if err != nil || recovery.Lease == nil || recovery.Lease.Scope().Generation != m.Generation(p.ID) || !recovery.Last.Completed || recovery.Last.ZNode == nil {
		t.Fatal("public recovery lost original receipt/generation", err)
	}
	t.Cleanup(func() { recovery.Lease.Close() })
	if got := nativeZooBuiltinProcesses(t, f); len(got) != len(before) {
		t.Fatal("explicit recovery connected or executed a request")
	}
	if _, err := recovery.Lease.Execute(context.Background(), prepared); !errors.Is(err, contract.ErrKVPlanUsed) {
		t.Fatal("recovery accepted an original prepared write")
	}
	read := nativeZooCall(t, recovery.Lease, "kv_get", map[string]string{"key": "/logical"})
	nativeZooObserveBuiltinLaunch(t, f, before)
	if !bytes.Equal(read.ZNode.Reply.Node.Value, []byte{0, 255, 128}) {
		t.Fatal("explicit recovered read lost original bytes")
	}
}
