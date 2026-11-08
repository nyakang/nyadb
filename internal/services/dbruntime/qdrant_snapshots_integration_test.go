package dbruntime

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/qdrant"
)

func qdrantSnapshotPublic(t *testing.T) (*Manager, contract.Session, contract.SnapshotProvider, contract.SnapshotLease, *qdrant.Client, string) {
	t.Helper()
	parsed := qdrantPublicFixture(t, "NYADB_QDRANT_CONFIG")
	cfg, _ := connections.QdrantProfileConfig(parsed.Profile)
	admin, err := qdrant.Open(context.Background(), cfg, parsed.Password)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { admin.Close() })
	name := "owned_public_snapshot_" + time.Now().Format("150405000000000")
	if _, err = admin.Do(context.Background(), qdrant.Request{Method: "PUT", Path: "/collections/" + name, ContentType: "application/json", Body: []byte(`{"vectors":{"size":2,"distance":"Dot"},"shard_number":1}`)}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.Do(context.Background(), qdrant.Request{Method: "DELETE", Path: "/collections/" + name})
	})
	if _, err = admin.Do(context.Background(), qdrant.Request{Method: "PUT", Path: "/collections/" + name + "/points?wait=true", ContentType: "application/json", Body: []byte(`{"points":[{"id":18446744073709551615,"vector":[1,0],"payload":{"n":9007199254740993,"text":"原公开快照🐱"}}]}`)}); err != nil {
		t.Fatal(err)
	}
	m := New()
	RegisterBuiltins(m, nil)
	t.Cleanup(func() { m.Close() })
	if err = m.Connect(context.Background(), parsed.Profile, parsed.Password); err != nil {
		t.Fatal(err)
	}
	s, err := m.ConnectSession(context.Background(), parsed.Profile, parsed.Password)
	if err != nil {
		t.Fatal(err)
	}
	p, err := m.Snapshots(s.ProfileID())
	if err != nil {
		t.Fatal(err)
	}
	o := contract.ObjectRef{ProfileID: s.ProfileID(), Kind: "snapshot_management", Name: "snapshot_management"}
	l, err := p.ReserveSnapshotManagementDocument(o, 70)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	if l.Generation() != s.Generation() || l.Generation() == s.(*session).raw.Generation() {
		t.Fatal("raw generation leaked into snapshots")
	}
	return m, s, p, l, admin, name
}
func publicSnapshotRead(t *testing.T, l contract.SnapshotLease, target contract.SnapshotTarget) contract.SnapshotObservation {
	t.Helper()
	o, err := l.Read(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	return o
}
func publicSnapshotPlan(t *testing.T, l contract.SnapshotLease, o contract.SnapshotObservation, d contract.SnapshotDraft) contract.SnapshotPlan {
	t.Helper()
	p, err := l.Prepare(o, d)
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func publicSnapshotPoint(t *testing.T, admin *qdrant.Client, name string) {
	t.Helper()
	count, _, err := admin.Count(context.Background(), name, nil)
	if err != nil || count != 1 {
		t.Fatal("restored original physical collection count", err, count)
	}
	point, _, err := admin.ReadPoint(context.Background(), name, json.RawMessage(`18446744073709551615`))
	if err != nil || string(point.ID) != "18446744073709551615" || !strings.Contains(string(point.Payload), "9007199254740993") || !strings.Contains(string(point.Payload), "原公开快照🐱") {
		t.Fatal("restored original exact point/payload", err)
	}
}
func TestQdrantNativePublicSnapshotsOriginalGenerationScopesTransfersAndRecovery(t *testing.T) {
	m, s, provider, l, admin, name := qdrantSnapshotPublic(t)
	ctx := context.Background()
	ref := m.Namespace(s.ProfileID(), name)
	qp, err := contract.Require[contract.QueryProvider](s)
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range []contract.SnapshotTarget{{Scope: contract.SnapshotCollection, Collection: ref}, {Scope: contract.SnapshotShard, Collection: ref}, {Scope: contract.SnapshotStorage}} {
		if !provider.SupportsSnapshots(l.Object(), target, "create") || provider.SupportsSnapshots(l.Object(), target, "upload") != (target.Scope != contract.SnapshotStorage) {
			t.Fatal("public snapshot capabilities")
		}
		data, err := m.Data(s.ProfileID()).ReserveDocument(ref.Object("points", "points"))
		if err != nil {
			t.Fatal(err)
		}
		before := publicSnapshotRead(t, l, target)
		plan := publicSnapshotPlan(t, l, before, contract.SnapshotDraft{Action: "create", Wait: true})
		r, err := l.Submit(ctx, plan)
		if err != nil || r.State != "response" || r.Receipt.Description == nil || r.Object != l.Object() || r.Target != target {
			t.Fatal("native public archive receipt", err, r.State)
		}
		if _, err = qp.ReserveQuery(71); !errors.Is(err, contract.ErrNamespaceBusy) {
			t.Fatal("public REST crossed archive claim", err)
		}
		if page, err := data.Runtime().AdvancedLoader(ctx, s.ProfileID(), name, "points", contract.DataOptions{Object: ref.Object("points", "points"), Limit: 1}); err != nil || len(page.Rows) != 1 {
			t.Fatal("public data read blocked by archive creation", err)
		}
		description := contract.CloneSnapshotDescription(*r.Receipt.Description)
		r.Receipt.Description.Raw[0] = 'x'
		r, err = l.Verify(ctx)
		if err != nil || r.State != "applied" || !r.Matched || r.Receipt.Description.Raw[0] == 'x' {
			t.Fatal("public archive readback", err)
		}
		before = publicSnapshotRead(t, l, target)
		plan = publicSnapshotPlan(t, l, before, contract.SnapshotDraft{Action: "download", Selected: description})
		path := filepath.Join(t.TempDir(), "original.snapshot")
		file, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		r, err = l.Download(ctx, plan, file, contract.SnapshotTransferOptions{MaxBytes: description.Size + 1, Timeout: 2 * time.Minute})
		if err != nil || r.State != "transferred" || !r.Transfer.Complete || r.Transfer.Received != description.Size || r.Transfer.Written != description.Size || r.Transfer.Target != target {
			t.Fatal("public streaming archive", err)
		}
		checksum := r.Transfer.SHA256
		if target.Scope == contract.SnapshotShard {
			before = publicSnapshotRead(t, l, target)
			plan = publicSnapshotPlan(t, l, before, contract.SnapshotDraft{Action: "stream"})
			stream, err := os.Create(filepath.Join(t.TempDir(), "shard.snapshot"))
			if err != nil {
				t.Fatal(err)
			}
			r, err = l.Download(ctx, plan, stream, contract.SnapshotTransferOptions{MaxBytes: 64 << 20, Timeout: 2 * time.Minute})
			if err != nil || !r.Transfer.Complete {
				t.Fatal("public direct shard stream", err)
			}
		}
		if target.Scope != contract.SnapshotStorage {
			before = publicSnapshotRead(t, l, target)
			plan = publicSnapshotPlan(t, l, before, contract.SnapshotDraft{Action: "upload", Wait: true, Source: contract.SnapshotSource{Name: "original-public.snapshot", Size: description.Size, SHA256: checksum}, Recovery: contract.SnapshotRecovery{Priority: "snapshot"}})
			source, err := os.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = l.Upload(ctx, plan, source, contract.SnapshotTransferOptions{MaxBytes: description.Size}); !errors.Is(err, qdrant.ErrManagementInUse) {
				t.Fatal("public recovery ignored original data document", err)
			}
			data.Close()
			if _, err = admin.Do(ctx, qdrant.Request{Method: "PUT", Path: "/collections/" + name + "/points?wait=true", ContentType: "application/json", Body: []byte(`{"points":[{"id":2,"vector":[0,1]}]}`)}); err != nil {
				t.Fatal(err)
			}
			source, err = os.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			r, err = l.Upload(ctx, plan, source, contract.SnapshotTransferOptions{MaxBytes: description.Size, Timeout: 2 * time.Minute})
			if err != nil || r.State != "response" || r.Receipt.SourceSHA256 != checksum || r.Receipt.SourceBytesRead != description.Size || r.Receipt.HasNullResult != (target.Scope == contract.SnapshotShard) {
				t.Fatal("public true/null original upload", err)
			}
			if _, err = m.Data(s.ProfileID()).ReserveDocument(ref.Object("points", "points")); !errors.Is(err, contract.ErrNamespaceBusy) {
				t.Fatal("public restored data reopened before observation", err)
			}
			r, err = l.Verify(ctx)
			if err != nil || r.State != "observed" || r.Matched || !r.HasCount || r.Count != 1 || r.Target != target {
				t.Fatal("public recovery fabricated full data proof", err)
			}
			publicSnapshotPoint(t, admin, name)
			before = publicSnapshotRead(t, l, target)
			location := "file:///qdrant/snapshots/" + name + "/" + description.Name
			if target.Scope == contract.SnapshotShard {
				location = description.Name
			}
			plan = publicSnapshotPlan(t, l, before, contract.SnapshotDraft{Action: "recover", Wait: true, Recovery: contract.SnapshotRecovery{Location: location, Checksum: checksum, Priority: "snapshot"}})
			if _, err = l.Submit(ctx, plan); err != nil {
				t.Fatal(err)
			}
			if r, err = l.Verify(ctx); err != nil || r.State != "observed" || r.Matched || r.Count != 1 {
				t.Fatal("public original URL/local recovery", err)
			}
		} else {
			data.Close()
			if _, err = l.Prepare(publicSnapshotRead(t, l, target), contract.SnapshotDraft{Action: "recover", Wait: true, Recovery: contract.SnapshotRecovery{Location: "file:///owned/full.snapshot"}}); !errors.Is(err, qdrant.ErrInvalid) {
				t.Fatal("invented public whole-storage REST recovery", err)
			}
		}
		before = publicSnapshotRead(t, l, target)
		plan = publicSnapshotPlan(t, l, before, contract.SnapshotDraft{Action: "delete", Wait: true, Selected: description})
		if _, err = l.Submit(ctx, plan); err != nil {
			t.Fatal(err)
		}
		if r, err = l.Verify(ctx); err != nil || r.State != "applied" || !r.Matched {
			t.Fatal("public archive delete", err)
		}
		if _, err = l.Submit(ctx, plan); !errors.Is(err, qdrant.ErrSnapshotUsed) {
			t.Fatal("public archive replayed", err)
		}
	}
}

type publicSnapshotClosedStream struct{ closed atomic.Int32 }

func (s *publicSnapshotClosedStream) Read([]byte) (int, error)  { return 0, os.ErrClosed }
func (s *publicSnapshotClosedStream) Write([]byte) (int, error) { return 0, os.ErrClosed }
func (s *publicSnapshotClosedStream) Close() error              { s.closed.Add(1); return nil }
func TestQdrantNativePublicSnapshotsRevocationRetainsReceiptAndClosesOriginalStreams(t *testing.T) {
	m, s, p, l, _, name := qdrantSnapshotPublic(t)
	target := contract.SnapshotTarget{Scope: contract.SnapshotCollection, Collection: m.Namespace(s.ProfileID(), name)}
	before := publicSnapshotRead(t, l, target)
	plan := publicSnapshotPlan(t, l, before, contract.SnapshotDraft{Action: "create", Wait: true})
	r, err := l.Submit(context.Background(), plan)
	if err != nil || r.Receipt.Description == nil {
		t.Fatal(err)
	}
	original := string(r.Receipt.Response.Body)
	r.Receipt.Response.Body[0] = 'x'
	r.Receipt.Description.Raw[0] = 'x'
	parsed := qdrantPublicFixture(t, "NYADB_QDRANT_CONFIG")
	newSession, err := m.ConnectSession(context.Background(), parsed.Profile, parsed.Password)
	if err != nil || newSession.Generation() <= l.Generation() {
		t.Fatal(err)
	}
	if p.SupportsSnapshots(l.Object(), target, "create") {
		t.Fatal("old snapshot provider followed replacement")
	}
	if _, err = l.Prepare(before, contract.SnapshotDraft{Action: "create"}); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal(err)
	}
	for _, upload := range []bool{false, true} {
		stream := &publicSnapshotClosedStream{}
		if upload {
			r, err = l.Upload(context.Background(), plan, stream, contract.SnapshotTransferOptions{MaxBytes: 100})
		} else {
			r, err = l.Download(context.Background(), plan, stream, contract.SnapshotTransferOptions{MaxBytes: 100})
		}
		if !errors.Is(err, contract.ErrRevoked) || stream.closed.Load() != 1 || r.State != "indeterminate" || string(r.Receipt.Response.Body) != original || r.Receipt.Description.Raw[0] == 'x' {
			t.Fatal("original rejected stream/evidence crossed replacement", err)
		}
	}
	if r, err = l.Verify(context.Background()); !errors.Is(err, contract.ErrRevoked) || string(r.Receipt.Response.Body) != original {
		t.Fatal("revocation lost original evidence", err)
	}
	current, err := m.Snapshots(s.ProfileID())
	if err != nil {
		t.Fatal(err)
	}
	newLease, err := current.ReserveSnapshotManagementDocument(l.Object(), 70)
	if err != nil {
		t.Fatal(err)
	}
	defer newLease.Close()
	if _, err = newLease.Prepare(before, contract.SnapshotDraft{Action: "create"}); !errors.Is(err, contract.ErrNamespaceChanged) {
		t.Fatal("new publication accepted old observation", err)
	}
	l.Close()
	l.Close()
	if newLease.Generation() != newSession.Generation() {
		t.Fatal("replacement generation changed")
	}
	if _, err = newLease.Read(context.Background(), target); err != nil || !m.IsConnected(s.ProfileID()) {
		t.Fatal("old close damaged replacement", err)
	}
}
