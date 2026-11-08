package qdrant

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

type snapshotHarness struct {
	mu                                sync.Mutex
	archives                          map[string]SnapshotDescription
	archive                           []byte
	config                            string
	count                             uint64
	falseDelete, failCount, lostWrite bool
	calls, mutations                  atomic.Int32
}

func (h *snapshotHarness) add(name string) SnapshotDescription {
	d, _ := parseSnapshotDescription([]byte(descriptionJSON(name, uint64(len(h.archive)), snapshotHash(h.archive))))
	h.archives[name] = d
	return d
}
func (h *snapshotHarness) handler(w http.ResponseWriter, r *http.Request) {
	h.calls.Add(1)
	h.mu.Lock()
	defer h.mu.Unlock()
	switch {
	case r.URL.Path == "/aliases":
		fmt.Fprint(w, snapshotEnvelope("{\"aliases\":[]}"))
	case r.URL.Path == "/collections/owned":
		fmt.Fprint(w, h.config)
	case strings.HasSuffix(r.URL.Path, "/points/count"):
		if h.failCount {
			w.WriteHeader(500)
			fmt.Fprint(w, "{\"status\":{\"error\":\"readback denied\"}}")
			return
		}
		fmt.Fprint(w, snapshotEnvelope(fmt.Sprintf("{\"count\":%d}", h.count)))
	case strings.HasSuffix(r.URL.Path, "/snapshots") && r.Method == "GET":
		rows := []json.RawMessage{}
		for _, d := range h.archives {
			rows = append(rows, d.Raw)
		}
		raw, _ := json.Marshal(rows)
		fmt.Fprint(w, snapshotEnvelope(string(raw)))
	case strings.HasSuffix(r.URL.Path, "/snapshots") && r.Method == "POST":
		h.mutations.Add(1)
		d := h.add("created.snapshot")
		if h.lostWrite {
			connection, _, _ := w.(http.Hijacker).Hijack()
			connection.Close()
			return
		}
		fmt.Fprint(w, snapshotEnvelope(string(d.Raw)))
	case strings.HasSuffix(r.URL.Path, "/upload"):
		h.mutations.Add(1)
		reader, err := r.MultipartReader()
		if err != nil {
			w.WriteHeader(400)
			return
		}
		part, err := reader.NextPart()
		if err != nil {
			w.WriteHeader(400)
			return
		}
		_, _ = io.Copy(io.Discard, part)
		h.add(part.FileName())
		if strings.Contains(r.URL.Path, "/shards/") {
			fmt.Fprint(w, snapshotEnvelope("null"))
		} else {
			fmt.Fprint(w, snapshotEnvelope("true"))
		}
	case strings.HasSuffix(r.URL.Path, "/recover"):
		h.mutations.Add(1)
		fmt.Fprint(w, snapshotEnvelope("true"))
	case strings.Contains(r.URL.Path, "/snapshots/") && r.Method == "DELETE":
		h.mutations.Add(1)
		name := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		if !h.falseDelete {
			delete(h.archives, name)
		}
		fmt.Fprint(w, snapshotEnvelope(fmt.Sprint(!h.falseDelete)))
	case strings.Contains(r.URL.Path, "/snapshots/") && r.Method == "GET" || strings.HasSuffix(r.URL.Path, "/snapshot"):
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(h.archive)
	default:
		w.WriteHeader(404)
	}
}
func unitSnapshotLease(t *testing.T) (*Session, *SnapshotLease, *snapshotHarness) {
	t.Helper()
	h := &snapshotHarness{archives: map[string]SnapshotDescription{}, archive: []byte("original archive"), config: sessionCollection, count: ^uint64(0)}
	h.add("original.snapshot")
	server := sessionServer(t, h.handler)
	s := testSession(t, connections.QdrantConfig{Endpoint: server.URL}, "")
	l, err := s.ReserveSnapshotDocument(10)
	if err != nil {
		t.Fatal(err)
	}
	return s, l, h
}
func snapshotRead(t *testing.T, l *SnapshotLease, target SnapshotTarget) SessionSnapshotObservation {
	t.Helper()
	o, err := l.Read(context.Background(), target)
	if err != nil || o.owner != l || o.value == nil {
		t.Fatal("original snapshot observation", err)
	}
	return o
}

func TestQdrantSnapshotSessionImmutableHandlesForeignPlansAndPhysicalTargets(t *testing.T) {
	s, l, h := unitSnapshotLease(t)
	o := snapshotRead(t, l, unitSnapshotTarget)
	m := o.Snapshot()
	if !bytes.Contains(m.Config, []byte("9007199254740993")) || len(m.Reads) != 4 {
		t.Fatal("native snapshot metadata lost")
	}
	first, err := l.PrepareCreate(o, true)
	if err != nil {
		t.Fatal(err)
	}
	p, err := l.PrepareRecovery(o, SnapshotRecovery{Location: "https://source.example/archive", APIKey: "private-source-key", Priority: "snapshot"}, true)
	if err != nil || bytes.Contains(p.Preview().Body, []byte("private-source-key")) {
		t.Fatal("prepared source key exposed", err)
	}
	calls := h.calls.Load()
	if _, err = l.Submit(context.Background(), first); !errors.Is(err, ErrSnapshotUsed) || h.calls.Load() != calls {
		t.Fatal("superseded preview dispatched", err)
	}
	other, _ := s.ReserveSnapshotDocument(11)
	if _, err = other.Submit(context.Background(), p); !errors.Is(err, ErrSnapshotUsed) || h.calls.Load() != calls {
		t.Fatal("foreign lease dispatched", err)
	}
	if _, err = l.PrepareCreate(SessionSnapshotObservation{owner: l, value: &snapshotObservation{metadata: m}}, true); !errors.Is(err, contract.ErrNamespaceChanged) {
		t.Fatal("forged handle accepted", err)
	}
	m.Collections[0] = "caller"
	m.Config[0] = 'x'
	m.Snapshots[0].Raw[0] = 'x'
	m.Reads[0].Response.Body[0] = 'x'
	if o.Snapshot().Collections[0] != "owned" || p.before.metadata.Config[0] != '{' || o.Snapshot().Snapshots[0].Raw[0] != '{' {
		t.Fatal("mutable public copies changed original authorization")
	}
	if _, err = l.PrepareDownload(o, SnapshotDescription{Name: "foreign.snapshot"}); !errors.Is(err, ErrSnapshotChanged) {
		t.Fatal("foreign description selected", err)
	}
	missing := snapshotRead(t, l, SnapshotTarget{Scope: CollectionSnapshots, Collection: "missing"})
	if _, err = l.PrepareCreate(missing, true); !errors.Is(err, contract.ErrNamespaceChanged) {
		t.Fatal("missing physical collection snapshotted", err)
	}
	if _, err = l.PrepareRecovery(missing, SnapshotRecovery{Location: "https://source.example/archive"}, true); err != nil {
		t.Fatal("new physical collection recovery rejected", err)
	}
	if _, err = s.ReserveManagementDocument(10); !errors.Is(err, ErrInvalid) {
		t.Fatal("duplicate cross-feature document admitted", err)
	}
	l.Close()
	if _, err = l.PrepareCreate(o, true); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("closed original lease prepared", err)
	}
}

func TestQdrantSnapshotSessionArchiveReceiptsReadbackAndEditingClaims(t *testing.T) {
	for _, falseDelete := range []bool{false, true} {
		t.Run(fmt.Sprint(falseDelete), func(t *testing.T) {
			s, l, h := unitSnapshotLease(t)
			h.falseDelete = falseDelete
			o := snapshotRead(t, l, unitSnapshotTarget)
			p, err := l.PrepareCreate(o, true)
			if err != nil {
				t.Fatal(err)
			}
			data, err := s.ReserveDataDocument(s.Namespace("owned").Object("points", "points"))
			if err != nil {
				t.Fatal(err)
			}
			r, err := l.Submit(context.Background(), p)
			if err != nil || r.State != "response" || r.Receipt.Description == nil || s.namespaceOwner != l || s.claimedCollections["owned"] {
				t.Fatal("archive creation closed data scope or lost receipt", err)
			}
			if _, err = s.ReserveQuery(22); !errors.Is(err, contract.ErrNamespaceBusy) {
				t.Fatal("REST crossed original archive claim", err)
			}
			r.Receipt.Description.Raw[0] = 'x'
			v, err := l.Verify(context.Background())
			if err != nil || v.State != "applied" || !v.Matched || v.Receipt.Description.Raw[0] != '{' || s.namespaceOwner != nil {
				t.Fatal("created archive verification", err, v.State)
			}
			data.Close()
			o = snapshotRead(t, l, unitSnapshotTarget)
			p, err = l.PrepareDelete(o, o.Snapshot().Snapshots[0], true)
			if err != nil {
				t.Fatal(err)
			}
			r, err = l.Submit(context.Background(), p)
			if err != nil || !r.Receipt.HasResult || r.Receipt.Result == falseDelete {
				t.Fatal("native boolean lost", err)
			}
			v, err = l.Verify(context.Background())
			want := "applied"
			if falseDelete {
				want = "unchanged"
			}
			if err != nil || v.State != want || !v.Matched || s.namespaceOwner != nil {
				t.Fatal("delete native boolean readback", err, v.State)
			}
			if _, err = l.Submit(context.Background(), p); !errors.Is(err, ErrSnapshotUsed) {
				t.Fatal("verified mutation replayed", err)
			}
		})
	}
}

func TestQdrantSnapshotSessionRecoveryClaimsNullReceiptAndReadbackWithoutDataProof(t *testing.T) {
	for _, scope := range []SnapshotScope{CollectionSnapshots, ShardSnapshots} {
		t.Run(string(scope), func(t *testing.T) {
			s, l, h := unitSnapshotLease(t)
			o := snapshotRead(t, l, SnapshotTarget{Scope: scope, Collection: "owned"})
			p, err := l.PrepareUpload(o, "restore.snapshot", uint64(len(h.archive)), snapshotHash(h.archive), "snapshot", true)
			if err != nil {
				t.Fatal(err)
			}
			data, _ := s.ReserveDataDocument(s.Namespace("owned").Object("points", "points"))
			calls := h.calls.Load()
			_, err = l.Upload(context.Background(), p, io.NopCloser(bytes.NewReader(h.archive)), SnapshotTransferOptions{MaxBytes: 100})
			if !errors.Is(err, ErrManagementInUse) || h.calls.Load() != calls {
				t.Fatal("restoration ignored original data document", err)
			}
			data.Close()
			rest, _ := s.ReserveQuery(99)
			_, err = l.Upload(context.Background(), p, io.NopCloser(bytes.NewReader(h.archive)), SnapshotTransferOptions{MaxBytes: 100})
			if !errors.Is(err, ErrManagementInUse) || h.calls.Load() != calls {
				t.Fatal("restoration ignored original REST document", err)
			}
			rest.Release()
			r, err := l.Upload(context.Background(), p, io.NopCloser(bytes.NewReader(h.archive)), SnapshotTransferOptions{MaxBytes: 100})
			if err != nil || r.State != "response" || r.Receipt.SourceSHA256 != snapshotHash(h.archive) {
				t.Fatal("native original recovery", err)
			}
			if r.Receipt.HasNullResult != (scope == ShardSnapshots) || r.Receipt.HasResult != (scope == CollectionSnapshots) {
				t.Fatal("null/boolean collapsed")
			}
			if _, err = s.ReserveDataDocument(s.Namespace("owned").Object("points", "points")); !errors.Is(err, contract.ErrNamespaceBusy) {
				t.Fatal("unverified affected document admitted", err)
			}
			other, err := s.ReserveDataDocument(s.Namespace("other").Object("points", "points"))
			if err != nil {
				t.Fatal(err)
			}
			other.Close()
			h.mu.Lock()
			h.failCount = true
			h.mu.Unlock()
			v, err := l.Verify(context.Background())
			if err == nil || v.Receipt.State != "completed" || s.namespaceOwner != l {
				t.Fatal("readback failure discarded original receipt", err)
			}
			h.mu.Lock()
			h.failCount = false
			h.mu.Unlock()
			v, err = l.Verify(context.Background())
			if err != nil || v.State != "observed" || v.Matched || !v.HasCount || v.Count != ^uint64(0) || s.namespaceOwner != nil {
				t.Fatal("live count fabricated complete data proof", err, v.State, v.Count)
			}
			if h.mutations.Load() != 1 {
				t.Fatal("readback mutated again")
			}
		})
	}
}

func TestQdrantSnapshotSessionPreflightDriftLostReceiptAndTransferOwnership(t *testing.T) {
	s, l, h := unitSnapshotLease(t)
	o := snapshotRead(t, l, unitSnapshotTarget)
	p, _ := l.PrepareCreate(o, true)
	h.mu.Lock()
	h.config = strings.Replace(sessionCollection, "9007199254740993", "9007199254740992", 1)
	h.mu.Unlock()
	r, err := l.Submit(context.Background(), p)
	if !errors.Is(err, contract.ErrNamespaceChanged) || r.State != "not_submitted" || h.mutations.Load() != 0 || s.namespaceOwner != nil {
		t.Fatal("changed original config dispatched", err)
	}
	h.mu.Lock()
	h.config = sessionCollection
	h.lostWrite = true
	h.mu.Unlock()
	o = snapshotRead(t, l, unitSnapshotTarget)
	p, _ = l.PrepareCreate(o, true)
	r, err = l.Submit(context.Background(), p)
	var interrupted *Interrupted
	if !errors.As(err, &interrupted) || r.State != "uncertain" || s.namespaceOwner != l {
		t.Fatal("lost receipt lost original claim", err)
	}
	v, err := l.Verify(context.Background())
	if err != nil || v.State != "uncertain" || v.Matched || h.mutations.Load() != 1 || s.namespaceOwner != l {
		t.Fatal("new archive invented missing native receipt", err)
	}
	if _, err = l.Submit(context.Background(), p); !errors.Is(err, contract.ErrNamespaceBusy) {
		t.Fatal("unresolved original write replayed", err)
	}
	l.Close()
	other, _ := s.ReserveSnapshotDocument(12)
	o = snapshotRead(t, other, unitSnapshotTarget)
	d := o.Snapshot().Snapshots[0]
	plan, _ := other.PrepareDownload(o, d)
	rest, _ := s.ReserveQuery(50)
	sink := newSnapshotSink()
	r, err = other.Download(context.Background(), plan, sink, SnapshotTransferOptions{MaxBytes: 100})
	if err != nil || r.State != "transferred" || !r.Transfer.Complete || sink.closed.Load() != 1 {
		t.Fatal("archive download incorrectly blocked by REST", err)
	}
	rest.Release()
	if _, err = other.Download(context.Background(), plan, newSnapshotSink(), SnapshotTransferOptions{MaxBytes: 100}); !errors.Is(err, ErrSnapshotUsed) {
		t.Fatal("old transfer plan replayed", err)
	}
}

func TestQdrantSnapshotSessionBoundsFailedObservationHasNoAuthority(t *testing.T) {
	_, l, h := unitSnapshotLease(t)
	h.mu.Lock()
	h.config = "{\"status\":\"ok\",\"result\":{\"config\":{\"blob\":\"" + strings.Repeat("x", 9<<20) + "\"}}}"
	h.mu.Unlock()
	o, err := l.Read(context.Background(), unitSnapshotTarget)
	if !errors.Is(err, ErrBounds) || o.owner != nil || l.observation != nil {
		t.Fatal("bounded partial metadata authorized work", err)
	}
	m := o.Snapshot()
	last := m.Reads[len(m.Reads)-1]
	if !last.BodyTruncated || last.OriginalBodyBytes <= len(last.Response.Body) || last.Response.Status != 200 {
		t.Fatal("bounded original read evidence lost")
	}
	if _, err = l.PrepareCreate(o, true); !errors.Is(err, contract.ErrNamespaceChanged) {
		t.Fatal("partial observation prepared", err)
	}
}

func TestQdrantSnapshotSessionOriginalSourceCloseDuringHandshakeWaitsAndReplacementSurvives(t *testing.T) {
	for _, closeKind := range []string{"cancel", "lease", "session"} {
		t.Run(closeKind, func(t *testing.T) {
			h := &snapshotHarness{archives: map[string]SnapshotDescription{}, archive: []byte("owned"), config: sessionCollection}
			h.add("original.snapshot")
			var block atomic.Bool
			entered, release := make(chan struct{}), make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == "/" {
					if block.CompareAndSwap(true, false) {
						close(entered)
						select {
						case <-release:
						case <-r.Context().Done():
						}
						return
					}
					fmt.Fprint(w, unitIdentity)
					return
				}
				if r.URL.Path == "/collections" {
					fmt.Fprint(w, sessionCollections)
					return
				}
				h.handler(w, r)
			}))
			t.Cleanup(server.Close)
			s := testSession(t, connections.QdrantConfig{Endpoint: server.URL}, "")
			l, _ := s.ReserveSnapshotDocument(10)
			o := snapshotRead(t, l, unitSnapshotTarget)
			p, _ := l.PrepareUpload(o, "owned.snapshot", 1, "", "snapshot", true)
			source := &blockingSnapshotSource{reading: make(chan struct{}), closing: make(chan struct{}), acknowledge: make(chan struct{}), release: make(chan struct{})}
			var once sync.Once
			ack := func() { once.Do(func() { close(source.acknowledge) }) }
			t.Cleanup(ack)
			defer close(release)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan SnapshotResult, 1)
			block.Store(true)
			go func() { r, _ := l.Upload(ctx, p, source, SnapshotTransferOptions{MaxBytes: 1}); done <- r }()
			waitSessionSignal(t, entered)
			closed := make(chan struct{})
			go func() {
				switch closeKind {
				case "cancel":
					cancel()
				case "lease":
					l.Close()
				case "session":
					s.Close()
				}
				close(closed)
			}()
			waitSessionSignal(t, source.closing)
			select {
			case <-done:
				t.Fatal("original upload settled before source close acknowledgement")
			default:
			}
			if closeKind != "cancel" {
				select {
				case <-closed:
					t.Fatal("original close settled early")
				default:
				}
			}
			ack()
			r := <-done
			if r.State != "not_submitted" || source.closes.Load() != 1 || source.reads.Load() != 0 || h.mutations.Load() != 0 {
				t.Fatal("preflight cancellation transmitted source")
			}
			waitSessionSignal(t, closed)
			replacement := testSession(t, connections.QdrantConfig{Endpoint: server.URL}, "")
			rl, _ := replacement.ReserveSnapshotDocument(10)
			calls := h.calls.Load()
			if _, err := rl.Upload(context.Background(), p, io.NopCloser(strings.NewReader("x")), SnapshotTransferOptions{MaxBytes: 1}); !errors.Is(err, ErrSnapshotUsed) || h.calls.Load() != calls {
				t.Fatal("replacement adopted old original preview", err)
			}
			if !replacement.Alive() {
				t.Fatal("original close revoked replacement")
			}
		})
	}
}
