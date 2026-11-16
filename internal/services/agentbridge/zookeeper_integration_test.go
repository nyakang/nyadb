package agentbridge_test

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/services/agentbridge"
	"github.com/nyakang/nyadb/internal/services/driverstore"
)

type zooFixture struct {
	Archive   string `json:"archive"`
	SHA256    string `json:"sha256"`
	Version   string `json:"version"`
	Directory string `json:"directory"`
	Endpoint  string `json:"endpoint"`
}

func realZooFixture(t *testing.T) zooFixture {
	t.Helper()
	if os.Getenv("NYADB_AGENT_ZOOKEEPER_IT") != "1" {
		t.Skip("requires owned original ZooKeeper agent and official ZooKeeper 3.9.4 fixture")
	}
	raw, err := os.ReadFile(os.Getenv("NYADB_AGENT_ZOOKEEPER_FIXTURE_FILE"))
	var f zooFixture
	if err != nil || json.Unmarshal(raw, &f) != nil || f.Archive == "" || f.SHA256 == "" || f.Directory == "" || f.Endpoint == "" {
		t.Fatal("original ZooKeeper fixture unavailable")
	}
	return f
}

func realZooRuntime(t *testing.T, f zooFixture) (*driverstore.Store, *driverstore.SingleSessionRuntime) {
	t.Helper()
	driverDir := t.TempDir()
	store, err := driverstore.New(driverDir)
	if err != nil {
		t.Fatal(err)
	}
	input, err := os.Open(f.Archive)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	m, err := store.Install(ctx, input, f.SHA256)
	_ = input.Close()
	if err != nil {
		t.Fatal(err)
	}
	l, err := store.Acquire(ctx, m.Key, m.Version, f.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	r, err := l.StartSingleSessionObserved(ctx, "zookeeper-v1", agentbridge.Options{MaxFrameBytes: 32 << 20, CancelWait: 5 * time.Second}, func(pid int) error {
		// A captured identity journal supports bounded cleanup after a killed
		// integration test. Historical PIDs alone never select live processes.
		stat, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
		if err != nil {
			return err
		}
		end := strings.LastIndexByte(string(stat), ')')
		if end < 0 {
			return agentbridge.ErrLaunch
		}
		fields := strings.Fields(string(stat[end+1:]))
		if len(fields) < 20 {
			return agentbridge.ErrLaunch
		}
		record, _ := json.Marshal(map[string]any{"pid": pid, "start_ticks": fields[19], "executable": filepath.Join(driverDir, "zookeeper@"+m.Version, "bin", "agent"), "version": m.Version})
		file, err := os.OpenFile(filepath.Join(f.Directory, "processes.jsonl"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
		if err != nil {
			return err
		}
		defer file.Close()
		_, err = file.Write(append(record, '\n'))
		return err
	})
	if err != nil {
		t.Fatal("original immutable runtime failed", err)
	}
	t.Cleanup(func() {
		if err := r.Close(); err != nil {
			t.Error("original runtime close", err)
		}
		if err := store.Remove(context.Background(), m.Key, m.Version); err != nil {
			t.Error("original package cleanup", err)
		}
	})
	if r.Session.Handshake().ProtocolVersion != 1 || !r.Session.Handshake().Supports("kv") || r.Session.Handshake().Supports("multi_session") {
		t.Fatal("native single-session capabilities changed")
	}
	return store, r
}

func zooParams(endpoint, namespace string) map[string]any {
	return map[string]any{"connection": map[string]any{"zookeeper_connect_string": endpoint, "namespace": namespace, "max_retries": 0, "base_sleep_time_ms": 0, "session_timeout_ms": 2000, "connection_timeout_ms": 5000}}
}

func connectZoo(t *testing.T, s *agentbridge.SingleSession, endpoint, namespace string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	r, err := s.Connect(ctx, zooParams(endpoint, namespace))
	if err != nil || !r.ExecutorAcknowledged {
		t.Fatal("original ZooKeeper connect", err)
	}
}

func callZoo(t *testing.T, s *agentbridge.SingleSession, method string, params any) agentbridge.Receipt {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	r, err := s.Call(ctx, method, params)
	if err != nil || !r.ExecutorAcknowledged || !json.Valid(r.OriginalResponse()) {
		t.Fatal("original ZooKeeper operation", method, err)
	}
	return r
}

func putZoo(t *testing.T, s *agentbridge.SingleSession, key, encoding, data, mode string) string {
	t.Helper()
	r := callZoo(t, s, "kv_put", map[string]any{"key": key, "value": map[string]string{"encoding": encoding, "data": data}, "writeMode": "create", "createMode": mode})
	var result struct {
		CreatedKey string `json:"createdKey"`
		Version    int    `json:"version"`
	}
	if json.Unmarshal(r.Result, &result) != nil || result.CreatedKey == "" || result.Version != 0 {
		t.Fatal("original created znode identity lost")
	}
	return result.CreatedKey
}

type zooNode struct {
	Found    bool                            `json:"found"`
	Key      string                          `json:"key"`
	Value    struct{ Encoding, Data string } `json:"value"`
	Metadata struct {
		Czxid, Mzxid, Pzxid, Ctime, Mtime, EphemeralOwner    int64
		Version, Cversion, Aversion, DataLength, NumChildren int32
	} `json:"metadata"`
}

func getZoo(t *testing.T, s *agentbridge.SingleSession, key string) zooNode {
	t.Helper()
	r := callZoo(t, s, "kv_get", map[string]string{"key": key})
	var n zooNode
	if json.Unmarshal(r.Result, &n) != nil {
		t.Fatal("native stat decode failed")
	}
	return n
}

func TestZooKeeperAgentNativeManagedValuesStatListingAndNamespace(t *testing.T) {
	f := realZooFixture(t)
	store, r := realZooRuntime(t, f)
	otherStore, other := realZooRuntime(t, f)
	probeCtx, probeCancel := context.WithTimeout(context.Background(), 10*time.Second)
	probe, probeErr := r.Session.TestConnection(probeCtx, zooParams(f.Endpoint, ""))
	probeCancel()
	if probeErr != nil || !probe.ExecutorAcknowledged || !strings.Contains(string(probe.Result), "3.9.4") {
		t.Fatal("original connection test did not verify real ZooKeeper", probeErr)
	}
	connectZoo(t, r.Session, f.Endpoint, "")
	connectZoo(t, other.Session, f.Endpoint, "")
	if err := store.Remove(context.Background(), "zookeeper", f.Version); !errors.Is(err, driverstore.ErrBusy) {
		t.Fatal("live process package removed", err)
	}
	info := callZoo(t, r.Session, "connection_info", nil)
	var product struct {
		DatabaseInfo struct{ ProductName, ProductVersion string } `json:"databaseInfo"`
	}
	if json.Unmarshal(info.Result, &product) != nil || product.DatabaseInfo.ProductName != "ZooKeeper" || !strings.HasPrefix(product.DatabaseInfo.ProductVersion, "3.9.4") {
		t.Fatal("actual ZooKeeper product/version not verified")
	}
	root := "/nyadb_" + rand.Text()
	putZoo(t, r.Session, root, "utf8", "root", "persistent")
	t.Cleanup(func() {
		if other.Session.Alive() {
			callZoo(t, other.Session, "kv_delete", map[string]any{"key": root, "recursive": true})
		}
	})
	binaryValue := []byte{0, 255, 128, 92, 39, 10}
	key := putZoo(t, r.Session, root+"/二进制", "base64", base64.StdEncoding.EncodeToString(binaryValue), "persistent")
	node := getZoo(t, other.Session, key)
	decoded, err := base64.StdEncoding.DecodeString(node.Value.Data)
	if err != nil || string(decoded) != string(binaryValue) || node.Value.Encoding != "base64" || node.Key != key || !node.Found || node.Metadata.Czxid <= 0 || node.Metadata.Mzxid != node.Metadata.Czxid || node.Metadata.DataLength != int32(len(binaryValue)) || node.Metadata.EphemeralOwner != 0 {
		t.Fatal("native binary/stat identity lost")
	}
	callZoo(t, r.Session, "kv_put", map[string]any{"key": key, "value": map[string]string{"encoding": "utf8", "data": "中文\n🙂"}, "writeMode": "update"})
	updated := getZoo(t, other.Session, key)
	if updated.Metadata.Czxid != node.Metadata.Czxid || updated.Metadata.Mzxid <= node.Metadata.Mzxid || updated.Metadata.Version != 1 || updated.Value.Data != "中文\n🙂" {
		t.Fatal("native update version/value lost")
	}
	putZoo(t, r.Session, root+"/empty", "base64", "", "persistent")
	empty := getZoo(t, other.Session, root+"/empty")
	if !empty.Found || empty.Value.Data != "" || empty.Metadata.DataLength != 0 {
		t.Fatal("empty znode confused with missing")
	}
	missing := getZoo(t, other.Session, root+"/missing")
	if missing.Found {
		t.Fatal("missing znode fabricated")
	}
	putZoo(t, r.Session, root+"/tree/child", "utf8", "child", "persistent")
	first := putZoo(t, r.Session, root+"/seq-", "utf8", "a", "persistent_sequential")
	second := putZoo(t, r.Session, root+"/seq-", "utf8", "b", "persistent_sequential")
	if first == second || !strings.HasPrefix(first, root+"/seq-") || !strings.HasPrefix(second, root+"/seq-") {
		t.Fatal("original sequential identities lost")
	}
	var keys []string
	continuation := ""
	for page := 0; page < 20; page++ {
		result := callZoo(t, r.Session, "kv_list_prefix", map[string]any{"prefix": root, "recursive": true, "limit": 2, "continuation": continuation})
		var list struct {
			Keys []struct {
				Key   string
				Czxid int64
			}
			Continuation *string
		}
		if json.Unmarshal(result.Result, &list) != nil || len(list.Keys) > 2 {
			t.Fatal("native page changed")
		}
		for _, row := range list.Keys {
			if row.Czxid <= 0 {
				t.Fatal("native listing stat lost")
			}
			keys = append(keys, row.Key)
		}
		if list.Continuation == nil {
			break
		}
		continuation = *list.Continuation
		if page == 19 {
			t.Fatal("native continuation did not terminate")
		}
	}
	if len(keys) != 6 {
		t.Fatal("native recursive listing incomplete", len(keys))
	}
	for _, deletion := range []struct {
		key       string
		recursive bool
		count     int64
	}{{second, false, 1}, {root + "/tree", true, 2}, {root + "/missing", false, 0}} {
		deleted := callZoo(t, r.Session, "kv_delete", map[string]any{"key": deletion.key, "recursive": deletion.recursive})
		var count struct {
			Deleted int64 `json:"deleted"`
		}
		if json.Unmarshal(deleted.Result, &count) != nil || count.Deleted != deletion.count || getZoo(t, other.Session, deletion.key).Found {
			t.Fatal("original native delete count or server state lost")
		}
	}
	putZoo(t, r.Session, root+"/chroot/ns", "utf8", "namespace", "persistent")
	_, scoped := realZooRuntime(t, f)
	connectZoo(t, scoped.Session, f.Endpoint+root+"/chroot", "ns")
	if n := getZoo(t, scoped.Session, "/"); !n.Found || n.Key != "/" || n.Value.Data != "namespace" {
		t.Fatal("logical chroot/namespace identity lost")
	}
	putZoo(t, scoped.Session, "/inside", "utf8", "scoped", "persistent")
	if n := getZoo(t, other.Session, root+"/chroot/ns/inside"); !n.Found || n.Value.Data != "scoped" {
		t.Fatal("namespace operation reached another root")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	failed, err := r.Session.Call(ctx, "kv_put", map[string]any{"key": key, "value": map[string]string{"encoding": "utf8", "data": "duplicate"}, "writeMode": "create"})
	if err == nil || !failed.ExecutorAcknowledged || failed.RemoteError == nil || failed.RemoteError.OperationOutcome != "" || !strings.Contains(string(failed.OriginalResponse()), "error") {
		t.Fatal("generic native write error was classified as a proved rollback")
	}
	// Only the agent's actual stat fields are evidence; no etcd cluster/header
	// identity or CAS precondition is claimed by this protocol acceptance.
	if err := otherStore.Remove(context.Background(), "zookeeper", f.Version); !errors.Is(err, driverstore.ErrBusy) {
		t.Fatal("independent live package removed")
	}
}

func TestZooKeeperAgentNativeEndpointRoutesRequireCompleteCapturedMapping(t *testing.T) {
	f := realZooFixture(t)
	logical := "nyadb-a.invalid:2181,nyadb-b.invalid:2181"
	for _, tc := range []struct {
		name   string
		routes map[string]string
	}{
		{"complete", map[string]string{"nyadb-a.invalid:2181": f.Endpoint, "nyadb-b.invalid:2181": f.Endpoint}},
		{"missing", map[string]string{"nyadb-a.invalid:2181": f.Endpoint}},
		{"nonloopback", map[string]string{"nyadb-a.invalid:2181": "192.0.2.1:2181", "nyadb-b.invalid:2181": f.Endpoint}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, runtime := realZooRuntime(t, f)
			params := zooParams(logical, "")
			params["connection"].(map[string]any)["zookeeper_endpoint_routes"] = tc.routes
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			r, err := runtime.Session.Connect(ctx, params)
			if tc.name == "complete" {
				if err != nil || !r.ExecutorAcknowledged || !getZoo(t, runtime.Session, "/").Found {
					t.Fatal("original routed logical endpoints failed", err)
				}
			} else if err == nil || !r.ExecutorAcknowledged || r.RemoteError == nil || !strings.Contains(string(r.OriginalResponse()), "endpoint route") {
				t.Fatal("invalid route mapping fell back to direct access", err)
			}
		})
	}
}

func TestZooKeeperAgentNativeEphemeralOwnershipAndExplicitProcessClose(t *testing.T) {
	f := realZooFixture(t)
	_, owner := realZooRuntime(t, f)
	_, observer := realZooRuntime(t, f)
	connectZoo(t, owner.Session, f.Endpoint, "")
	connectZoo(t, observer.Session, f.Endpoint, "")
	root := "/nyadb_ephemeral_" + rand.Text()
	putZoo(t, observer.Session, root, "utf8", "persistent", "persistent")
	t.Cleanup(func() { callZoo(t, observer.Session, "kv_delete", map[string]any{"key": root, "recursive": true}) })
	a := putZoo(t, owner.Session, root+"/temporary", "utf8", "owner", "ephemeral")
	b := putZoo(t, owner.Session, root+"/temporary-", "utf8", "sequence", "ephemeral_sequential")
	first, second := getZoo(t, observer.Session, a), getZoo(t, observer.Session, b)
	if !first.Found || !second.Found || first.Metadata.EphemeralOwner == 0 || first.Metadata.EphemeralOwner != second.Metadata.EphemeralOwner {
		t.Fatal("original server session owner lost")
	}
	if err := owner.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-owner.Session.Exited():
	default:
		t.Fatal("original agent not reaped")
	}
	deadline := time.Now().Add(10 * time.Second)
	for getZoo(t, observer.Session, a).Found || getZoo(t, observer.Session, b).Found {
		if time.Now().After(deadline) {
			t.Fatal("server did not expire original ephemeral nodes")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if n := getZoo(t, observer.Session, root); !n.Found || n.Value.Data != "persistent" {
		t.Fatal("process close removed unrelated persistent state")
	}
}

// zooDropRelay reads actual ZooKeeper Jute frames. It forwards the request to
// the real server and withholds one successful create acknowledgment. Observing
// the node through a separate original agent then proves a committed lost reply.
type zooDropRelay struct {
	listener net.Listener
	target   string
	drop     atomic.Bool
	creates  atomic.Int32
	ack      chan struct{}
	mu       sync.Mutex
	pairs    map[net.Conn]net.Conn
	closing  bool
	wait     sync.WaitGroup
}

func newZooDropRelay(t *testing.T, target string) *zooDropRelay {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	r := &zooDropRelay{listener: l, target: target, ack: make(chan struct{}, 1), pairs: map[net.Conn]net.Conn{}}
	r.wait.Add(1)
	go func() {
		defer r.wait.Done()
		for {
			down, err := l.Accept()
			if err != nil {
				return
			}
			r.wait.Add(1)
			go r.serve(down)
		}
	}()
	t.Cleanup(func() {
		_ = l.Close()
		r.mu.Lock()
		r.closing = true
		for down, up := range r.pairs {
			_ = down.Close()
			_ = up.Close()
		}
		r.mu.Unlock()
		r.wait.Wait()
	})
	return r
}

func zooFrame(conn net.Conn) ([]byte, error) {
	var length [4]byte
	if _, err := io.ReadFull(conn, length[:]); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint32(length[:])
	if n > 32<<20 {
		return nil, agentbridge.ErrLimit
	}
	raw := make([]byte, 4+int(n))
	copy(raw, length[:])
	_, err := io.ReadFull(conn, raw[4:])
	return raw, err
}

func (r *zooDropRelay) serve(down net.Conn) {
	defer r.wait.Done()
	up, err := net.DialTimeout("tcp", r.target, 2*time.Second)
	if err != nil {
		_ = down.Close()
		return
	}
	r.mu.Lock()
	if r.closing {
		r.mu.Unlock()
		_ = down.Close()
		_ = up.Close()
		return
	}
	r.pairs[down] = up
	r.mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer func() { _ = down.Close(); _ = up.Close(); r.mu.Lock(); delete(r.pairs, down); r.mu.Unlock() }()
	var requestsMu sync.Mutex
	creates := map[uint32]bool{}
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		defer cancel()
		defer up.Close()
		defer down.Close()
		handshake := true
		for {
			raw, err := zooFrame(down)
			if err != nil {
				return
			}
			if handshake {
				handshake = false
			} else if len(raw) >= 12 {
				if binary.BigEndian.Uint32(raw[8:12]) == 1 {
					r.creates.Add(1)
					requestsMu.Lock()
					creates[binary.BigEndian.Uint32(raw[4:8])] = true
					requestsMu.Unlock()
				}
			}
			if _, err := up.Write(raw); err != nil {
				return
			}
		}
	}()
	defer func() { _ = up.Close(); _ = down.Close(); <-finished }()
	handshake := true
	for {
		raw, err := zooFrame(up)
		if err != nil {
			return
		}
		if handshake {
			handshake = false
		} else if len(raw) >= 20 {
			requestsMu.Lock()
			create := creates[binary.BigEndian.Uint32(raw[4:8])]
			delete(creates, binary.BigEndian.Uint32(raw[4:8]))
			requestsMu.Unlock()
			if create && binary.BigEndian.Uint32(raw[16:20]) == 0 && r.drop.CompareAndSwap(true, false) {
				r.ack <- struct{}{}
				<-ctx.Done()
				return
			}
		}
		if _, err := down.Write(raw); err != nil {
			return
		}
	}
}

func TestZooKeeperAgentNativeCommittedLostReplyCancellationQueueAndRecovery(t *testing.T) {
	f := realZooFixture(t)
	_, observer := realZooRuntime(t, f)
	connectZoo(t, observer.Session, f.Endpoint, "")
	root := "/nyadb_lost_" + rand.Text()
	putZoo(t, observer.Session, root, "utf8", "persistent", "persistent")
	t.Cleanup(func() { callZoo(t, observer.Session, "kv_delete", map[string]any{"key": root, "recursive": true}) })
	relay := newZooDropRelay(t, f.Endpoint)
	_, original := realZooRuntime(t, f)
	connectZoo(t, original.Session, relay.listener.Addr().String(), "")
	relay.drop.Store(true)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type outcome struct {
		receipt agentbridge.Receipt
		err     error
	}
	done := make(chan outcome, 1)
	go func() {
		r, err := original.Session.Call(ctx, "kv_put", map[string]any{"key": root + "/committed", "value": map[string]string{"encoding": "utf8", "data": "original committed value"}, "writeMode": "create", "createMode": "persistent"})
		done <- outcome{r, err}
	}()
	select {
	case <-relay.ack:
	case <-time.After(10 * time.Second):
		t.Fatal("actual server create acknowledgment not observed")
	}
	if n := getZoo(t, observer.Session, root+"/committed"); !n.Found || n.Value.Data != "original committed value" || n.Metadata.Version != 0 {
		t.Fatal("lost reply was not a real committed write")
	}
	queuedCtx, queuedCancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer queuedCancel()
	queued, err := original.Session.Call(queuedCtx, "kv_delete", map[string]string{"key": root})
	if !errors.Is(err, context.DeadlineExceeded) || queued.Dispatched || !original.Session.Alive() {
		t.Fatal("queued cancellation reached original active executor", err)
	}
	select {
	case <-done:
		t.Fatal("unknown write settled before executor response or process exit")
	default:
	}
	cancel()
	out := <-done
	if !errors.Is(out.err, context.Canceled) || !out.receipt.Dispatched || !out.receipt.CancellationRequested || out.receipt.ExecutorAcknowledged || !out.receipt.ProcessExitConfirmed || len(out.receipt.OriginalResponse()) != 0 {
		t.Fatal("real unknown write lost its original disposition", out.err)
	}
	if relay.creates.Load() != 1 {
		t.Fatal("native write retried", relay.creates.Load())
	}
	if r, err := original.Session.Call(context.Background(), "kv_put", nil); !errors.Is(err, agentbridge.ErrClosed) || r.Dispatched {
		t.Fatal("original process replayed a write")
	}
	_, recovered := realZooRuntime(t, f)
	connectZoo(t, recovered.Session, f.Endpoint, "")
	if n := getZoo(t, recovered.Session, root+"/committed"); !n.Found || n.Value.Data != "original committed value" || n.Metadata.Version != 0 {
		t.Fatal("explicit recovery replayed or changed the committed node")
	}
	if !observer.Session.Alive() {
		t.Fatal("original cancellation revoked independent observer")
	}
}
