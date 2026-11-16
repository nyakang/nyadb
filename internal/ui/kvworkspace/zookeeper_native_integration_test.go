package kvworkspace

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"image/png"
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

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/agentbridge"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/driverstore"
	"github.com/nyakang/nyadb/internal/services/zookeeper"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

type nativeZooFixture struct {
	Archive, SHA256, Version, Directory, Endpoint string
	options                                       zookeeper.Options
	processes                                     *atomic.Int32
}

func nativeZooSetup(t *testing.T) nativeZooFixture {
	t.Helper()
	if os.Getenv("NYADB_ZOOKEEPER_PROVIDER_IT") != "1" {
		t.Skip("requires owned official ZooKeeper and original managed agent")
	}
	raw, err := os.ReadFile(os.Getenv("NYADB_AGENT_ZOOKEEPER_FIXTURE_FILE"))
	var f nativeZooFixture
	if err != nil || json.Unmarshal(raw, &f) != nil || f.Endpoint == "" || f.Archive == "" || f.Directory == "" {
		t.Fatal("native ZooKeeper fixture unavailable")
	}
	directory := t.TempDir()
	store, err := driverstore.New(directory)
	if err != nil {
		t.Fatal(err)
	}
	input, err := os.Open(f.Archive)
	if err != nil {
		t.Fatal(err)
	}
	m, err := store.Install(context.Background(), input, f.SHA256)
	input.Close()
	if err != nil {
		t.Fatal(err)
	}
	f.processes = &atomic.Int32{}
	f.options = zookeeper.Options{Driver: connections.ManagedDriver{Key: m.Key, Version: m.Version, SHA256: f.SHA256, Directory: directory, Platform: m.Platform, Profiles: m.Profiles}, Started: func(pid int) error {
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
		record, _ := json.Marshal(map[string]any{"pid": pid, "start_ticks": fields[19], "executable": filepath.Join(directory, "zookeeper@"+m.Version, "bin", "agent"), "version": m.Version})
		journal, err := os.OpenFile(filepath.Join(f.Directory, "processes.jsonl"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
		if err != nil {
			return err
		}
		defer journal.Close()
		_, err = journal.Write(append(record, '\n'))
		f.processes.Add(1)
		return err
	}}
	t.Cleanup(func() {
		if err := store.Remove(context.Background(), m.Key, m.Version); err != nil {
			t.Error("captured package still leased", err)
		}
	})
	return f
}

func nativeZooSession(t *testing.T, f nativeZooFixture, c zookeeper.Config) *zookeeper.Session {
	t.Helper()
	if c.ConnectString == "" {
		c.ConnectString = f.Endpoint
	}
	c.SessionTimeoutMS, c.ConnectionTimeoutMS = 2000, 5000
	s, err := zookeeper.OpenNativeSession(context.Background(), 7, c, "", f.options)
	if err != nil {
		t.Fatal("native provider open", err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error("native provider close", err)
		}
	})
	if strings.SplitN(s.KVScope().Version, "-", 2)[0] != "3.9.4" || s.KVScope().Header != (contract.KVHeader{}) || s.KVScope().HTTP.Protocol != "" {
		t.Fatal("original product version or native scope changed", s.KVScope().Version)
	}
	t.Log("actual native product version:", s.KVScope().Version)
	return s
}

func nativeZooLease(t *testing.T, s *zookeeper.Session, id uint64) contract.KVRequestLease {
	t.Helper()
	l, err := s.ReserveKV(id)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := l.Close(); err != nil {
			t.Error("native document close", err)
		}
	})
	return l
}

func nativeZooPlan(t *testing.T, l contract.KVRequestLease, method string, params any) contract.KVPreparation {
	t.Helper()
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	p, err := l.Prepare(contract.KVRequest{Method: method, JSON: string(raw)})
	if err != nil {
		t.Fatal("original request prepare", err)
	}
	return p
}

func nativeZooCall(t *testing.T, l contract.KVRequestLease, method string, params any) contract.KVResult {
	t.Helper()
	p := nativeZooPlan(t, l, method, params)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	r, err := l.Execute(ctx, p)
	if err != nil || !r.Completed || r.ZNode == nil || !r.ZNode.Captured || r.ZNode.Reply == nil || !json.Valid(r.ZNode.Native) || r.ProjectionError != "" {
		t.Fatal("native operation or complete projection", method, err, r.Code, r.ProjectionError)
	}
	return r
}

func nativeZooPut(key string, value []byte, mode string) map[string]any {
	return map[string]any{"key": key, "value": map[string]string{"encoding": "base64", "data": base64.StdEncoding.EncodeToString(value)}, "writeMode": "create", "createMode": mode}
}

func TestZooKeeperNativeProviderOriginalValuesNamespaceListingAndDocumentOwnership(t *testing.T) {
	f := nativeZooSetup(t)
	s := nativeZooSession(t, f, zookeeper.Config{})
	observer := nativeZooLease(t, s, 1)
	root := "/nyadb_provider_" + rand.Text()
	nativeZooCall(t, observer, "kv_put", nativeZooPut(root, []byte("root"), "persistent"))
	t.Cleanup(func() { nativeZooCall(t, observer, "kv_delete", map[string]any{"key": root, "recursive": true}) })
	original := nativeZooLease(t, s, 2)
	other := nativeZooLease(t, s, 3)
	before := f.processes.Load()
	p := nativeZooPlan(t, original, "kv_get", map[string]string{"key": root})
	if f.processes.Load() != before {
		t.Fatal("prepare connected a document")
	}
	if _, err := other.Execute(context.Background(), p); !errors.Is(err, contract.ErrKVPlanUsed) {
		t.Fatal("request transferred to another document", err)
	}
	if _, err := original.Execute(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	if _, err := original.Execute(context.Background(), p); !errors.Is(err, contract.ErrKVPlanUsed) {
		t.Fatal("one-use original plan replayed", err)
	}
	for i, value := range [][]byte{{0, 255, 128}, []byte("中文🌸"), {}} {
		key := fmt.Sprintf("%s/value%d", root, i)
		nativeZooCall(t, original, "kv_put", nativeZooPut(key, value, "persistent"))
		r := nativeZooCall(t, observer, "kv_get", map[string]string{"key": key})
		n := r.ZNode.Reply.Node
		if !*r.ZNode.Reply.Found || !bytes.Equal(n.Value, value) || n.Value == nil || n.Stat.Version != 0 || int(n.Stat.DataLength) != len(value) || n.Stat.EphemeralOwner != 0 {
			t.Fatal("binary, Unicode, empty or stat lost")
		}
	}
	first := nativeZooCall(t, original, "kv_put", nativeZooPut(root+"/session-a", []byte("a"), "ephemeral"))
	second := nativeZooCall(t, other, "kv_put", nativeZooPut(root+"/session-b", []byte("b"), "ephemeral_sequential"))
	a := nativeZooCall(t, observer, "kv_get", map[string]string{"key": first.ZNode.Reply.CreatedKey}).ZNode.Reply.Node
	b := nativeZooCall(t, observer, "kv_get", map[string]string{"key": second.ZNode.Reply.CreatedKey}).ZNode.Reply.Node
	if a.Stat.EphemeralOwner == 0 || b.Stat.EphemeralOwner == 0 || a.Stat.EphemeralOwner == b.Stat.EphemeralOwner {
		t.Fatal("documents do not own independent actual server sessions")
	}
	list := nativeZooCall(t, observer, "kv_list_prefix", map[string]any{"prefix": root, "recursive": false, "limit": 2}).ZNode.Reply
	if len(list.Keys) != 2 || list.Continuation == nil || list.Keys[0].Stat == nil {
		t.Fatal("original listing/stat/continuation incomplete")
	}
	next := nativeZooCall(t, observer, "kv_list_prefix", map[string]any{"prefix": root, "recursive": false, "limit": 2, "continuation": *list.Continuation}).ZNode.Reply
	if len(next.Keys) == 0 || next.Keys[0].Key <= list.Keys[1].Key {
		t.Fatal("original sorted continuation changed")
	}
	if err := original.Close(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for *nativeZooCall(t, observer, "kv_get", map[string]string{"key": first.ZNode.Reply.CreatedKey}).ZNode.Reply.Found {
		if time.Now().After(deadline) {
			t.Fatal("closed original document still owns an ephemeral node")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !*nativeZooCall(t, observer, "kv_get", map[string]string{"key": second.ZNode.Reply.CreatedKey}).ZNode.Reply.Found {
		t.Fatal("closing original document reached another owner")
	}
	// The original root combines connection chroot and namespace; Prefix only
	// supplies browsing text and never prepends keys to a request.
	nativeZooCall(t, observer, "kv_put", nativeZooPut(root+"/nested", nil, "persistent"))
	namespaced := nativeZooSession(t, f, zookeeper.Config{ConnectString: f.Endpoint + root, Namespace: "/nested", Prefix: "/suggestion"})
	if namespaced.KVScope().ZNode.Root != root+"/nested" {
		t.Fatal("combined native root lost")
	}
	l := nativeZooLease(t, namespaced, 1)
	nativeZooCall(t, l, "kv_put", nativeZooPut("/logical", []byte("exact"), "persistent"))
	if n := nativeZooCall(t, observer, "kv_get", map[string]string{"key": root + "/nested/logical"}).ZNode.Reply.Node; string(n.Value) != "exact" {
		t.Fatal("native namespace or absolute key changed")
	}
}

func nativeZooConsume(t *testing.T, s *State, d *Document) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for d.Busy() || d.FileBusy() {
		s.Consume()
		if time.Now().After(deadline) {
			t.Fatal("original workbench document did not settle", d.Error())
		}
		time.Sleep(time.Millisecond)
	}
}

func nativeZooScreenshot(t *testing.T, f nativeZooFixture, s *State, d *Document, stage string) {
	t.Helper()
	defer i18n.SetLocaleFunc(nil)
	for _, lang := range []string{"zh-Hans", "en"} {
		i18n.SetLocaleFunc(func() string { return lang })
		for _, size := range [][2]int{{720, 520}, {1280, 800}} {
			tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.View(c, d); s.Modals(c) }, size[0], size[1])
			tt.Frame()
			labels := []string{"KV 原生回执", "复制完整操作记录"}
			if d.confirmation != nil {
				labels = []string{"我确认执行此原生写入请求", "确认执行 KV 写入"}
			}
			for _, label := range labels {
				r, ok := tt.Find(locales.Text(label))
				if !ok || r.W <= 0 || r.H <= 0 || r.X+r.W > float32(size[0])+1 || r.Y+r.H > float32(size[1])+1 {
					t.Fatal("native UI control clipped", label, stage, lang, size)
				}
			}
			path := filepath.Join(f.Directory, fmt.Sprintf("%s-%dx%d-%s.png", lang, size[0], size[1], stage))
			file, err := os.Create(path)
			if err != nil {
				t.Fatal(err)
			}
			err = png.Encode(file, tt.Image())
			closeErr := file.Close()
			if err != nil || closeErr != nil {
				t.Fatal("native screenshot capture failed")
			}
		}
	}
}

func TestZooKeeperNativeWorkspaceConfirmationParkedReceiptFilesAndUnknownRecovery(t *testing.T) {
	f := nativeZooSetup(t)
	p := nativeZooSession(t, f, zookeeper.Config{})
	observer := nativeZooLease(t, nativeZooSession(t, f, zookeeper.Config{}), 99)
	root := "/nyadb_workspace_" + rand.Text()
	nativeZooCall(t, observer, "kv_put", nativeZooPut(root, nil, "persistent"))
	t.Cleanup(func() { nativeZooCall(t, observer, "kv_delete", map[string]any{"key": root, "recursive": true}) })
	var active uint64 = 1
	s := New(Dependencies{ProviderFor: func(int64) (contract.KVRequestsProvider, error) { return p, nil }, Active: func(d *Document) bool { return d.ID() == active }})
	t.Cleanup(s.CloseAll)
	d, parked := s.Open(1, 7, contract.KVRequest{}), s.Open(2, 7, contract.KVRequest{})
	if d.Request().Method != "kv_list_prefix" || len(s.MethodsFor(d)) != 5 || d.SnapshotAvailable() {
		t.Fatal("native presentation contains foreign protobuf tools")
	}
	s.SetMethod(d, "kv_put")
	body, _ := json.Marshal(nativeZooPut(root+"/exact", []byte{0, 255, 128}, "persistent"))
	d.Editor.Buffer.Set(string(body))
	before := f.processes.Load()
	s.Run(d)
	if d.confirmation == nil || f.processes.Load() != before {
		t.Fatal("write preparation connected or dispatched")
	}
	nativeZooScreenshot(t, f, s, d, "confirmation")
	d.Editor.Buffer.Set(`{"key":"/foreign"}`)
	s.Confirm(d, true)
	if d.Busy() || f.processes.Load() != before {
		t.Fatal("edited confirmation dispatched original or replacement text")
	}
	d.Editor.Buffer.Set(string(body))
	s.Run(d)
	s.Confirm(d, true)
	active = parked.ID()
	deadline := time.Now().Add(10 * time.Second)
	for !*nativeZooCall(t, observer, "kv_get", map[string]string{"key": root + "/exact"}).ZNode.Reply.Found {
		if time.Now().After(deadline) {
			t.Fatal("parked original write did not commit")
		}
		time.Sleep(time.Millisecond)
	}
	s.Consume()
	if d.last != nil || !d.Busy() || parked.last != nil {
		t.Fatal("parked receipt consumed by active document")
	}
	active = d.ID()
	nativeZooConsume(t, s, d)
	if d.last == nil || !d.last.Result.Completed || d.last.Result.ZNode.Reply.CreatedKey != root+"/exact" {
		t.Fatal("original parked acknowledgment missing")
	}
	nativeZooScreenshot(t, f, s, d, "receipt")
	var saved bytes.Buffer
	var name string
	s.SetFiles(Files{SavePath: func(n string) (string, error) { name = n; return "original", nil }, WriteStream: func(_ string, write func(io.Writer) error) error { return write(&saved) }})
	original := bytes.Clone(d.last.Result.ZNode.Native)
	s.ExportReceipt(d, true)
	nativeZooConsume(t, s, d)
	if name != "response.jsonrpc.json" || !bytes.Equal(saved.Bytes(), original) {
		t.Fatal("native export changed original receipt")
	}
	originalRequest := d.Request()
	saved.Reset()
	s.SaveRequest(d, true)
	nativeZooConsume(t, s, d)
	requestFile := bytes.Clone(saved.Bytes())
	if loaded, err := DecodeRequest(requestFile); err != nil || loaded != originalRequest || !strings.HasSuffix(name, ".nyakv") {
		t.Fatal("native request file did not preserve original authored text", err, d.Error())
	}
	s.SetMethod(d, "kv_get")
	d.Editor.Buffer.Set(`{"key":"/temporary-draft"}`)
	s.SetFiles(Files{OpenPath: func() (string, error) { return "original", nil }, OpenReader: func(string) (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(requestFile)), nil }})
	s.OpenRequest(d)
	nativeZooConsume(t, s, d)
	if d.Request() != originalRequest || d.Dirty() {
		t.Fatal("native request file did not restore its original method and text", d.Error())
	}
	// A real generic original error (create of an existing node) carries no
	// dispatch-stage proof. Preserve it separately from all later observations.
	s.Run(d)
	s.Confirm(d, true)
	nativeZooConsume(t, s, d)
	if d.unknown == nil || !d.unknown.Result.WriteOutcomeUnknown || !d.unknown.Result.ZNode.Acknowledged {
		t.Fatal("generic native write error was presented as a proved rollback")
	}
	unknown := bytes.Clone(d.unknown.Result.ZNode.Native)
	s.SetMethod(d, "kv_get")
	d.Editor.Buffer.Set(fmt.Sprintf(`{"key":%q}`, root+"/exact"))
	s.Run(d)
	nativeZooConsume(t, s, d)
	if d.last == nil || !d.last.Result.Completed || !bytes.Equal(d.unknown.Result.ZNode.Native, unknown) {
		t.Fatal("later read overwrote original uncertain write")
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	s.DisconnectProfile(7)
	p = nativeZooSession(t, f, zookeeper.Config{})
	s.Recover(d)
	nativeZooConsume(t, s, d)
	if d.NeedsRecovery() || !bytes.Equal(d.unknown.Result.ZNode.Native, unknown) || !d.last.Result.Completed {
		t.Fatal("explicit recovery lost original independent evidence", d.Error())
	}
	s.SetMethod(d, "kv_put")
	d.Editor.Buffer.Set(string(body))
	s.Run(d)
	if d.confirmation != nil || d.Busy() {
		t.Fatal("recovery or read unlocked uncertain native writes")
	}
}

// A Jute relay observes actual committed Create replies; it never fabricates
// a server response or substitutes a protocol implementation.
type nativeZooDropRelay struct {
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

func newNativeZooDropRelay(t *testing.T, target string) *nativeZooDropRelay {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	r := &nativeZooDropRelay{listener: l, target: target, ack: make(chan struct{}, 1), pairs: map[net.Conn]net.Conn{}}
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

func nativeZooFrame(conn io.Reader) ([]byte, error) {
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

func (r *nativeZooDropRelay) serve(down net.Conn) {
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
	// connection_info discovers the actual version over a separate original
	// four-letter srvr connection. Forward that protocol transparently too.
	var prefix [4]byte
	if _, err := io.ReadFull(down, prefix[:]); err != nil {
		return
	}
	if string(prefix[:]) == "srvr" || string(prefix[:]) == "ruok" {
		if _, err := up.Write(prefix[:]); err != nil {
			return
		}
		_, _ = io.Copy(down, up)
		return
	}
	input := io.MultiReader(bytes.NewReader(prefix[:]), down)
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
			raw, err := nativeZooFrame(input)
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
		raw, err := nativeZooFrame(up)
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

func TestZooKeeperNativeWorkspaceCommittedLostReplyCancellationRecoveryAndNoReplay(t *testing.T) {
	f := nativeZooSetup(t)
	observer := nativeZooLease(t, nativeZooSession(t, f, zookeeper.Config{}), 99)
	root := "/nyadb_provider_lost_" + rand.Text()
	nativeZooCall(t, observer, "kv_put", nativeZooPut(root, nil, "persistent"))
	t.Cleanup(func() { nativeZooCall(t, observer, "kv_delete", map[string]any{"key": root, "recursive": true}) })
	relay := newNativeZooDropRelay(t, f.Endpoint)
	p := nativeZooSession(t, f, zookeeper.Config{ConnectString: relay.listener.Addr().String()})
	var active uint64 = 1
	s := New(Dependencies{ProviderFor: func(int64) (contract.KVRequestsProvider, error) { return p, nil }, Active: func(d *Document) bool { return d.ID() == active }})
	t.Cleanup(s.CloseAll)
	d, other := s.Open(1, 7, contract.KVRequest{}), s.Open(2, 7, contract.KVRequest{})
	s.Preconnect(d)
	nativeZooConsume(t, s, d)
	if d.NeedsRecovery() {
		t.Fatal("original physical document did not connect", d.Error())
	}
	relay.drop.Store(true)
	body, _ := json.Marshal(nativeZooPut(root+"/committed", []byte("original committed value"), "persistent"))
	s.SetMethod(d, "kv_put")
	d.Editor.Buffer.Set(string(body))
	s.Run(d)
	s.Confirm(d, true)
	select {
	case <-relay.ack:
	case <-time.After(10 * time.Second):
		t.Fatal("real server Create acknowledgment not observed")
	}
	n := nativeZooCall(t, observer, "kv_get", map[string]string{"key": root + "/committed"}).ZNode.Reply.Node
	if string(n.Value) != "original committed value" || n.Stat.Version != 0 {
		t.Fatal("dropped response did not belong to a committed original write")
	}
	active = other.ID()
	s.RequestCancel(d)
	finished := d.job.finished
	select {
	case <-finished:
	case <-time.After(10 * time.Second):
		t.Fatal("original executor/process did not settle cancellation")
	}
	s.Consume()
	if d.unknown != nil || !d.Busy() || other.last != nil || !p.Alive() {
		t.Fatal("parked cancellation consumed by another document or root session lost")
	}
	active = d.ID()
	nativeZooConsume(t, s, d)
	if d.unknown == nil || !d.unknown.Result.WriteOutcomeUnknown || !d.unknown.Result.ZNode.ProcessExitConfirmed || d.unknown.Result.ZNode.Acknowledged || !d.NeedsRecovery() {
		t.Fatal("committed lost reply became rollback or acknowledgment")
	}
	nativeZooScreenshot(t, f, s, d, "unknown-write")
	before := f.processes.Load()
	s.Recover(d)
	nativeZooConsume(t, s, d)
	if d.NeedsRecovery() || f.processes.Load() != before || d.unknown == nil {
		t.Fatal("explicit recovery opened/replayed or discarded unknown evidence", d.Error())
	}
	s.SetMethod(d, "kv_get")
	d.Editor.Buffer.Set(fmt.Sprintf(`{"key":%q}`, root+"/committed"))
	s.Run(d)
	nativeZooConsume(t, s, d)
	if d.last == nil || !d.last.Result.Completed || d.last.Result.ZNode.Reply.Node.Stat.Version != 0 || d.unknown == nil || relay.creates.Load() != 1 {
		t.Fatal("recovery read replayed original Create or overwrote uncertainty")
	}
	s.SetMethod(d, "kv_put")
	d.Editor.Buffer.Set(string(body))
	s.Run(d)
	if d.confirmation != nil || d.Busy() || relay.creates.Load() != 1 {
		t.Fatal("later read enabled a write after an unknown receipt")
	}
}
