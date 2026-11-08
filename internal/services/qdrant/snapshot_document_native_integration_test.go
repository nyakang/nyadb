package qdrant

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func nativeSnapshotLease(t *testing.T) (*Session, *SnapshotLease) {
	t.Helper()
	cfg, key, _ := nativeFixture(t)
	s := testSession(t, cfg, key)
	l, err := s.ReserveSnapshotDocument(10)
	if err != nil {
		t.Fatal(err)
	}
	return s, l
}
func nativeBoundSnapshotFile(t *testing.T, l *SnapshotLease, target SnapshotTarget, d SnapshotDescription) (string, SnapshotResult) {
	t.Helper()
	o := snapshotRead(t, l, target)
	p, err := l.PrepareDownload(o, d)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "original.snapshot")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	r, err := l.Download(context.Background(), p, file, SnapshotTransferOptions{MaxBytes: d.Size + 1, Timeout: 2 * time.Minute})
	if err != nil || r.State != "transferred" || !r.Transfer.Complete || r.Transfer.Received != d.Size || r.Transfer.Written != d.Size {
		t.Fatal("original bound snapshot transfer", err, r.State)
	}
	file, err = os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	hash := sha256.New()
	n, err := io.Copy(hash, file)
	if err != nil || uint64(n) != d.Size || hex.EncodeToString(hash.Sum(nil)) != r.Transfer.SHA256 {
		t.Fatal("owned native archive differs", err)
	}
	return path, r
}
func nativeBoundArchive(t *testing.T, l *SnapshotLease, target SnapshotTarget) SnapshotDescription {
	t.Helper()
	o := snapshotRead(t, l, target)
	p, err := l.PrepareCreate(o, true)
	if err != nil {
		t.Fatal(err)
	}
	r, err := l.Submit(context.Background(), p)
	if err != nil || r.State != "response" || r.Receipt.Description == nil {
		t.Fatal("native original snapshot receipt", err, r.State)
	}
	d := cloneSnapshotDescription(*r.Receipt.Description)
	v, err := l.Verify(context.Background())
	if err != nil || v.State != "applied" || !v.Matched {
		t.Fatal("native original archive readback", err, v.State)
	}
	return d
}

func TestQdrantNativeSnapshotSessionOriginalScopesArchivesTransfersAndRecovery(t *testing.T) {
	s, l := nativeSnapshotLease(t)
	admin := nativeOpen(t)
	name := nativeCollection(t, admin, "{\"vectors\":{\"size\":2,\"distance\":\"Dot\"},\"shard_number\":1}")
	nativeUpsert(t, admin, name, "{\"points\":[{\"id\":18446744073709551615,\"vector\":[1,0],\"payload\":{\"n\":9007199254740993,\"text\":\"原快照🐱\"}}]}")
	for _, target := range []SnapshotTarget{{Scope: CollectionSnapshots, Collection: name}, {Scope: ShardSnapshots, Collection: name, Shard: 0}, {Scope: StorageSnapshots}} {
		data := reservePointData(t, s, name)
		o := snapshotRead(t, l, target)
		p, err := l.PrepareCreate(o, true)
		if err != nil {
			t.Fatal(err)
		}
		r, err := l.Submit(context.Background(), p)
		if err != nil || r.State != "response" || r.Receipt.Description == nil || s.namespaceOwner != l || s.claimedCollections[name] {
			t.Fatal("snapshot creation affected original data", err)
		}
		if count, err := data.count(context.Background(), s.ProfileID(), name, "points"); err != nil || count != 1 {
			t.Fatal("original data could not read during archive creation", err, count)
		}
		page := nativeWritePointPage(t, data)
		edit := pointEdit("update", `{"id":18446744073709551615,"vector":[1,0],"payload":{"n":9007199254740993,"text":"归档后的原网格编辑🐱"}}`, page.RowSnapshots[0])
		saved := nativePointSave(t, data, []contract.DataEdit{edit})
		if verified := nativePointVerify(t, data, saved); verified.Status != "applied" {
			t.Fatal("original data could not save/verify during archive creation", verified.Status)
		}
		if _, err = s.ReserveQuery(30); !errors.Is(err, contract.ErrNamespaceBusy) {
			t.Fatal("REST crossed original archive claim", err)
		}
		manager, _ := s.ReserveManagementDocument(40)
		before := nativeManagedRead(t, manager, name)
		change, err := manager.PrepareCollection(before, name, "collection_update", []byte("{\"params\":{\"on_disk_payload\":true}}"), 30)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = manager.Submit(context.Background(), change); !errors.Is(err, contract.ErrNamespaceBusy) {
			t.Fatal("collection management crossed snapshot claim", err)
		}
		manager.Close()
		d := cloneSnapshotDescription(*r.Receipt.Description)
		r.Receipt.Description.Raw[0] = 'x'
		v, err := l.Verify(context.Background())
		if err != nil || v.State != "applied" || !v.Matched || v.Receipt.Description.Raw[0] != '{' || s.namespaceOwner != nil {
			t.Fatal("native archive evidence/readback", err, v.State)
		}
		path, transfer := nativeBoundSnapshotFile(t, l, target, d)
		data.Close()
		if target.Scope != StorageSnapshots {
			nativeUpsert(t, admin, name, "{\"points\":[{\"id\":2,\"vector\":[0,1]}]}")
			o = snapshotRead(t, l, target)
			p, err = l.PrepareUpload(o, "bound-original.snapshot", d.Size, transfer.Transfer.SHA256, "snapshot", true)
			if err != nil {
				t.Fatal(err)
			}
			rest, _ := s.ReserveQuery(50)
			source, err := os.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = l.Upload(context.Background(), p, source, SnapshotTransferOptions{MaxBytes: d.Size}); !errors.Is(err, ErrManagementInUse) {
				t.Fatal("restore ignored original REST document", err)
			}
			rest.Release()
			source, err = os.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			r, err = l.Upload(context.Background(), p, source, SnapshotTransferOptions{MaxBytes: d.Size, Timeout: 2 * time.Minute})
			if err != nil || r.State != "response" || r.Receipt.SourceBytesRead != d.Size || r.Receipt.HasNullResult != (target.Scope == ShardSnapshots) {
				t.Fatal("native bound upload receipt", err, r.State)
			}
			if _, err = s.ReserveDataDocument(s.Namespace(name).Object("points", "points")); !errors.Is(err, contract.ErrNamespaceBusy) {
				t.Fatal("restored original data reopened before readback", err)
			}
			v, err = l.Verify(context.Background())
			if err != nil || v.State != "observed" || v.Matched || !v.HasCount || v.Count != 1 || s.namespaceOwner != nil {
				t.Fatal("native recovery live observation fabricated full proof", err, v.State)
			}
			nativeSnapshotCount(t, admin, name, 1)
			nativeUpsert(t, admin, name, "{\"points\":[{\"id\":2,\"vector\":[0,1]}]}")
			o = snapshotRead(t, l, target)
			location := "file:///qdrant/snapshots/" + name + "/" + d.Name
			if target.Scope == ShardSnapshots {
				location = d.Name
			}
			p, err = l.PrepareRecovery(o, SnapshotRecovery{Location: location, Checksum: transfer.Transfer.SHA256, Priority: "snapshot"}, true)
			if err != nil {
				t.Fatal(err)
			}
			r, err = l.Submit(context.Background(), p)
			if err != nil || !r.Receipt.HasResult || !r.Receipt.Result {
				t.Fatal("native bound local recovery boolean", err)
			}
			v, err = l.Verify(context.Background())
			if err != nil || v.State != "observed" || v.Count != 1 {
				t.Fatal("native local recovery readback", err)
			}
			nativeSnapshotCount(t, admin, name, 1)
		} else {
			o = snapshotRead(t, l, target)
			if _, err = l.PrepareRecovery(o, SnapshotRecovery{Location: "file:///owned/full.snapshot"}, true); !errors.Is(err, ErrInvalid) {
				t.Fatal("invented full storage REST recovery", err)
			}
		}
		o = snapshotRead(t, l, target)
		p, err = l.PrepareDelete(o, d, true)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = l.Submit(context.Background(), p); err != nil {
			t.Fatal(err)
		}
		v, err = l.Verify(context.Background())
		if err != nil || v.State != "applied" || !v.Matched {
			t.Fatal("native original archive delete verification", err, v.State)
		}
		if _, err = l.Submit(context.Background(), p); !errors.Is(err, ErrSnapshotUsed) {
			t.Fatal("original archive deletion replayed", err)
		}
	}
}

func nativeSnapshotSessionRelay(t *testing.T, upstream *Client, after func(http.ResponseWriter, *http.Request, Response) bool) connections.QdrantConfig {
	t.Helper()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, requestLimit+1))
		if err != nil || len(body) > requestLimit {
			w.WriteHeader(502)
			return
		}
		req, err := upstream.transferRequest(r.Context(), Request{Method: r.Method, Path: r.URL.RequestURI(), ContentType: r.Header.Get("Content-Type")}, bytes.NewReader(body), int64(len(body)))
		if err != nil {
			w.WriteHeader(502)
			return
		}
		res, err := upstream.http.Do(req)
		if err != nil {
			w.WriteHeader(502)
			return
		}
		defer res.Body.Close()
		raw, err := io.ReadAll(io.LimitReader(res.Body, responseLimit+1))
		response := Response{Status: res.StatusCode, ContentType: res.Header.Get("Content-Type"), Body: raw, Incomplete: err != nil || len(raw) > responseLimit}
		if after(w, r, response) {
			return
		}
		w.Header().Set("Content-Type", response.ContentType)
		w.WriteHeader(response.Status)
		_, _ = w.Write(raw)
	}))
	t.Cleanup(server.Close)
	ca := filepath.Join(t.TempDir(), "relay.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	return connections.QdrantConfig{Endpoint: server.URL, Auth: "none", CAFile: ca, ServerName: "example.com"}
}

func TestQdrantNativeSnapshotSessionPreflightPermissionsAndFailedRecoveryEvidence(t *testing.T) {
	s, l := nativeSnapshotLease(t)
	admin := nativeOpen(t)
	name := nativeCollection(t, admin, "{\"vectors\":{\"size\":2,\"distance\":\"Dot\"},\"on_disk_payload\":false}")
	target := SnapshotTarget{Scope: CollectionSnapshots, Collection: name}
	o := snapshotRead(t, l, target)
	p, _ := l.PrepareCreate(o, true)
	update, err := PrepareCollectionManagement(name, "collection_update", []byte("{\"params\":{\"on_disk_payload\":true}}"), 30)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = admin.SubmitManagement(context.Background(), update); err != nil {
		t.Fatal(err)
	}
	r, err := l.Submit(context.Background(), p)
	if !errors.Is(err, contract.ErrNamespaceChanged) || r.State != "not_submitted" || s.namespaceOwner != nil {
		t.Fatal("native changed collection accepted original preview", err)
	}
	d := nativeBoundArchive(t, l, target)
	o = snapshotRead(t, l, target)
	download, _ := l.PrepareDownload(o, d)
	deleted, _ := PrepareSnapshotDelete(target, d.Name, true)
	nativeSnapshotSubmit(t, admin, deleted)
	sink := newSnapshotSink()
	if _, err = l.Download(context.Background(), download, sink, SnapshotTransferOptions{MaxBytes: d.Size}); !errors.Is(err, contract.ErrNamespaceChanged) || sink.closed.Load() != 1 || sink.count != 0 {
		t.Fatal("changed selected original archive downloaded", err)
	}
	cfg, _, readonly := nativeFixture(t)
	if cfg.Auth == "api_key" {
		reader := testSession(t, cfg, readonly)
		readLease, err := reader.ReserveSnapshotDocument(10)
		if err != nil {
			t.Fatal(err)
		}
		o = snapshotRead(t, readLease, target)
		p, err = readLease.PrepareCreate(o, true)
		if err != nil {
			t.Fatal(err)
		}
		r, err = readLease.Submit(context.Background(), p)
		if err == nil || r.State != "uncertain" || r.Receipt.Response.Status != 403 || len(r.Receipt.Response.Body) == 0 {
			t.Fatal("native denial lost original evidence", err)
		}
		if _, err = readLease.Submit(context.Background(), p); !errors.Is(err, contract.ErrNamespaceBusy) {
			t.Fatal("denied original snapshot replayed", err)
		}
		readLease.Close()
	}
	o = snapshotRead(t, l, target)
	p, err = l.PrepareUpload(o, "failed-bound.snapshot", 7, snapshotHash([]byte("invalid")), "snapshot", true)
	if err != nil {
		t.Fatal(err)
	}
	r, err = l.Upload(context.Background(), p, io.NopCloser(strings.NewReader("invalid")), SnapshotTransferOptions{MaxBytes: 7})
	if err == nil || r.State != "uncertain" || r.Receipt.Response.Status < 400 || r.Receipt.SourceBytesRead != 7 {
		t.Fatal("native failed archive recovery lost evidence", err)
	}
	v, err := l.Verify(context.Background())
	if err != nil || v.State != "uncertain" || l.pending == nil {
		t.Fatal("failed original recovery invented rollback", err)
	}
	found := false
	for _, d := range v.Metadata.Snapshots {
		found = found || d.Name == "failed-bound.snapshot"
	}
	if !found {
		t.Fatal("native failed upload residue discarded")
	}
}

func TestQdrantNativeSnapshotSessionLostCanceledTransfersAndOriginalCloseNoReplay(t *testing.T) {
	for _, action := range []string{"create", "delete", "upload", "recover", "download", "stream"} {
		for _, fault := range []string{"lost", "canceled", "lease_close", "session_close"} {
			t.Run(action+"/"+fault, func(t *testing.T) {
				admin := nativeOpen(t)
				name := nativeCollection(t, admin, "{\"vectors\":{\"size\":2,\"distance\":\"Dot\"},\"shard_number\":1}")
				nativeUpsert(t, admin, name, "{\"points\":[{\"id\":18446744073709551615,\"vector\":[1,0],\"payload\":{\"n\":9007199254740993,\"text\":\"原快照🐱\"}}]}")
				target := SnapshotTarget{Scope: CollectionSnapshots, Collection: name}
				d := nativeSnapshotCreate(t, admin, target)
				path, fileEvidence := nativeSnapshotFile(t, admin, target, d)
				raw, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if action == "upload" || action == "recover" {
					nativeUpsert(t, admin, name, "{\"points\":[{\"id\":2,\"vector\":[0,1]}]}")
				}
				var attempts atomic.Int32
				entered, acked, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
				cfg := nativeSnapshotSessionRelay(t, admin, func(w http.ResponseWriter, r *http.Request, response Response) bool {
					match := false
					switch action {
					case "create":
						match = r.Method == "POST" && r.URL.Path == "/collections/"+name+"/snapshots"
					case "delete":
						match = r.Method == "DELETE" && strings.HasSuffix(r.URL.Path, "/"+d.Name)
					case "upload":
						match = r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/upload")
					case "recover":
						match = r.Method == "PUT" && strings.HasSuffix(r.URL.Path, "/recover")
					case "download":
						match = r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/"+d.Name)
					case "stream":
						match = r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/shards/0/snapshot")
					}
					if !match {
						return false
					}
					attempts.Add(1)
					if action == "download" || action == "stream" {
						w.Header().Set("Content-Type", response.ContentType)
						w.Header().Set("Content-Length", fmt.Sprint(len(response.Body)))
						w.WriteHeader(response.Status)
						_, _ = w.Write(response.Body[:min(1024, len(response.Body))])
						w.(http.Flusher).Flush()
					}
					close(entered)
					if fault == "lost" {
						connection, _, _ := w.(http.Hijacker).Hijack()
						connection.Close()
					} else {
						select {
						case <-r.Context().Done():
						case <-release:
						}
					}
					close(acked)
					return true
				})
				defer close(release)
				s := testSession(t, cfg, "")
				l, _ := s.ReserveSnapshotDocument(10)
				if action == "stream" {
					target = SnapshotTarget{Scope: ShardSnapshots, Collection: name}
				}
				o := snapshotRead(t, l, target)
				var p SessionSnapshotPlan
				switch action {
				case "create":
					p, err = l.PrepareCreate(o, true)
				case "delete":
					p, err = l.PrepareDelete(o, d, true)
				case "upload":
					p, err = l.PrepareUpload(o, "original.snapshot", uint64(len(raw)), fileEvidence.SHA256, "snapshot", true)
				case "recover":
					p, err = l.PrepareRecovery(o, SnapshotRecovery{Location: "file:///qdrant/snapshots/" + name + "/" + d.Name, Checksum: fileEvidence.SHA256, Priority: "snapshot"}, true)
				case "download":
					p, err = l.PrepareDownload(o, d)
				case "stream":
					p, err = l.PrepareStream(o)
				}
				if err != nil {
					t.Fatal(err)
				}
				nativeCfg, key, _ := nativeFixture(t)
				replacement := testSession(t, nativeCfg, key)
				query, err := replacement.ReserveQuery(100)
				if err != nil {
					t.Fatal(err)
				}
				defer query.Release()
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				type outcome struct {
					r   SnapshotResult
					err error
				}
				done := make(chan outcome, 1)
				sink := &nativeStartedSnapshotSink{snapshotSink: newSnapshotSink(), started: make(chan struct{})}
				go func() {
					var r SnapshotResult
					var e error
					if action == "upload" {
						r, e = l.Upload(ctx, p, io.NopCloser(bytes.NewReader(raw)), SnapshotTransferOptions{MaxBytes: uint64(len(raw))})
					} else if action == "download" || action == "stream" {
						r, e = l.Download(ctx, p, sink, SnapshotTransferOptions{MaxBytes: 64 << 20})
					} else {
						r, e = l.Submit(ctx, p)
					}
					done <- outcome{r, e}
				}()
				waitSessionSignal(t, entered)
				read := action == "download" || action == "stream"
				if read && fault != "lost" {
					waitSessionSignal(t, sink.started)
				}
				if fault == "canceled" {
					cancel()
				}
				if fault == "lease_close" {
					l.Close()
				}
				if fault == "session_close" {
					s.Close()
				}
				got := <-done
				waitSessionSignal(t, acked)
				var interrupted *Interrupted
				if !errors.As(got.err, &interrupted) || interrupted.WriteOutcomeUnknown == read || got.r.ProfileID != s.ProfileID() {
					t.Fatal("original snapshot fault classification", got.err, got.r.State)
				}
				if read {
					if got.r.State != "transfer_incomplete" || got.r.Transfer.Complete || got.r.Transfer.Received == 0 || sink.closed.Load() != 1 {
						t.Fatal("original partial archive evidence lost")
					}
				} else {
					if got.r.State != "uncertain" || got.r.Receipt.State != "uncertain" {
						t.Fatal("original native write uncertainty cleared", got.r.State)
					}
					if fault == "lost" || fault == "canceled" {
						v, err := l.Verify(context.Background())
						if err != nil || v.State != "uncertain" || l.pending == nil {
							t.Fatal("live observations invented missing native receipt", err)
						}
						if _, err = l.Submit(context.Background(), p); action != "upload" && !errors.Is(err, contract.ErrNamespaceBusy) {
							t.Fatal("unknown original write replayed", err)
						}
					}
					if action == "upload" || action == "recover" {
						nativeSnapshotCount(t, admin, name, 1)
					}
					if action == "delete" {
						rows, _, err := admin.ListSnapshots(context.Background(), target)
						if err != nil {
							t.Fatal(err)
						}
						for _, candidate := range rows {
							if candidate.Name == d.Name {
								t.Fatal("actual original deletion missing")
							}
						}
					}
					if action == "create" {
						rows, _, err := admin.ListSnapshots(context.Background(), target)
						if err != nil || len(rows) != 2 {
							t.Fatal("actual original snapshot creation missing", err, len(rows))
						}
					}
				}
				if attempts.Load() != 1 {
					t.Fatal("original snapshot request repeated")
				}
				if _, err = query.Execute(context.Background(), "", "GET /collections"); err != nil {
					t.Fatal("original close affected replacement session", err)
				}
				foreign, _ := replacement.ReserveSnapshotDocument(10)
				if action == "upload" {
					_, err = foreign.Upload(context.Background(), p, io.NopCloser(bytes.NewReader(raw)), SnapshotTransferOptions{MaxBytes: uint64(len(raw))})
				} else if read {
					_, err = foreign.Download(context.Background(), p, newSnapshotSink(), SnapshotTransferOptions{MaxBytes: 64 << 20})
				} else {
					_, err = foreign.Submit(context.Background(), p)
				}
				if !errors.Is(err, ErrSnapshotUsed) {
					t.Fatal("replacement accepted original operation", err)
				}
			})
		}
	}
}

func TestQdrantNativeSnapshotSessionReadbackFaultsKeepOriginalReceiptAndNoReplay(t *testing.T) {
	for _, action := range []string{"create", "upload"} {
		for _, fault := range []string{"lost", "canceled", "binary_changed"} {
			t.Run(action+"/"+fault, func(t *testing.T) {
				admin := nativeOpen(t)
				name := nativeCollection(t, admin, "{\"vectors\":{\"size\":2,\"distance\":\"Dot\"}}")
				nativeUpsert(t, admin, name, `{"points":[{"id":18446744073709551615,"vector":[1,0],"payload":{"n":9007199254740993,"text":"原快照🐱"}}]}`)
				target := SnapshotTarget{Scope: CollectionSnapshots, Collection: name}
				var archive SnapshotDescription
				var source []byte
				if action == "upload" {
					archive = nativeSnapshotCreate(t, admin, target)
				}
				var enabled atomic.Bool
				var writes atomic.Int32
				entered, acked, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
				cfg := nativeSnapshotSessionRelay(t, admin, func(w http.ResponseWriter, r *http.Request, response Response) bool {
					if r.Method == "POST" && (strings.HasSuffix(r.URL.Path, "/snapshots") || strings.HasSuffix(r.URL.Path, "/snapshots/upload")) {
						writes.Add(1)
					}
					matches := r.URL.Path == "/collections/"+name
					if action == "upload" {
						matches = r.URL.Path == "/collections/"+name+"/points/count"
					}
					if fault == "binary_changed" {
						matches = r.URL.Path == "/"
					}
					if !matches || !enabled.CompareAndSwap(true, false) {
						return false
					}
					close(entered)
					if fault == "binary_changed" {
						w.Header().Set("Content-Type", "application/json")
						_, _ = w.Write(bytes.Replace(response.Body, []byte("1.20.0"), []byte("1.20.1"), 1))
					} else if fault == "lost" {
						connection, _, _ := w.(http.Hijacker).Hijack()
						connection.Close()
					} else {
						select {
						case <-r.Context().Done():
						case <-release:
						}
					}
					close(acked)
					return true
				})
				defer close(release)
				s := testSession(t, cfg, "")
				l, _ := s.ReserveSnapshotDocument(10)
				o := snapshotRead(t, l, target)
				p, _ := l.PrepareCreate(o, true)
				var submitted SnapshotResult
				var err error
				if action == "upload" {
					path, transfer := nativeBoundSnapshotFile(t, l, target, archive)
					source, err = os.ReadFile(path)
					if err != nil {
						t.Fatal(err)
					}
					o = snapshotRead(t, l, target)
					p, err = l.PrepareUpload(o, "readback-original.snapshot", archive.Size, transfer.Transfer.SHA256, "snapshot", true)
					if err != nil {
						t.Fatal(err)
					}
					submitted, err = l.Upload(context.Background(), p, io.NopCloser(bytes.NewReader(source)), SnapshotTransferOptions{MaxBytes: archive.Size, Timeout: 2 * time.Minute})
				} else {
					submitted, err = l.Submit(context.Background(), p)
				}
				if err != nil || action == "create" && submitted.Receipt.Description == nil || action == "upload" && (submitted.Receipt.SourceBytesRead != archive.Size || submitted.Receipt.SourceSHA256 == "") {
					t.Fatal(err)
				}
				enabled.Store(true)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				type outcome struct {
					r   SnapshotResult
					err error
				}
				done := make(chan outcome, 1)
				go func() { r, e := l.Verify(ctx); done <- outcome{r, e} }()
				waitSessionSignal(t, entered)
				if fault == "canceled" {
					cancel()
				}
				got := <-done
				waitSessionSignal(t, acked)
				if got.err == nil || got.r.State != "indeterminate" || string(got.r.Receipt.Response.Body) != string(submitted.Receipt.Response.Body) || got.r.Receipt.SourceSHA256 != submitted.Receipt.SourceSHA256 || l.pending == nil {
					t.Fatal("original receipt changed after readback fault", got.err, got.r.State)
				}
				if fault == "binary_changed" {
					if !errors.Is(got.err, contract.ErrRevoked) || s.Alive() {
						t.Fatal("changed native binary retained original session", got.err)
					}
				} else {
					v, err := l.Verify(context.Background())
					if err != nil || action == "create" && (v.State != "applied" || !v.Matched) || action == "upload" && (v.State != "observed" || v.Matched || !v.HasCount || v.Count != 1) {
						t.Fatal("explicit original readback retry failed", err)
					}
				}
				if writes.Load() != 1 {
					t.Fatal("readback resubmitted snapshot mutation")
				}
			})
		}
	}
}
