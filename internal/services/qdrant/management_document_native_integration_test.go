package qdrant

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func nativeManagedLease(t *testing.T) (*Session, *ManagementLease) {
	t.Helper()
	cfg, secret, _ := nativeFixture(t)
	s := testSession(t, cfg, secret)
	l, err := s.ReserveManagementDocument(1)
	if err != nil {
		t.Fatal(err)
	}
	return s, l
}
func nativeManagedRead(t *testing.T, l *ManagementLease, targets ...string) ManagementObservation {
	t.Helper()
	o, err := l.Read(context.Background(), targets)
	if err != nil || len(o.Token) != 64 {
		t.Fatal("original native management observation", err)
	}
	return o
}
func nativeManagedAction(t *testing.T, l *ManagementLease, name, action string) ManagedPlan {
	t.Helper()
	target := name
	if action == "collection_create" {
		target += "_created"
	}
	o := nativeManagedRead(t, l, target)
	var p ManagedPlan
	var err error
	switch action {
	case "collection_create":
		p, err = l.PrepareCollection(o, target, action, []byte(`{"vectors":{"size":2,"distance":"Dot"},"on_disk_payload":false}`), 30)
	case "collection_update":
		p, err = l.PrepareCollection(o, target, action, []byte(`{"params":{"on_disk_payload":true}}`), 30)
	case "collection_delete":
		p, err = l.PrepareCollection(o, target, action, nil, 30)
	case "aliases":
		p, err = l.PrepareAliases(o, []AliasChange{{Action: "create_alias", Alias: name + "_alias", Collection: name}}, 30)
	}
	if err != nil || p.native.used == nil {
		t.Fatal("original native managed plan", err)
	}
	return p
}
func nativeManagedSubmit(t *testing.T, l *ManagementLease, p ManagedPlan) ManagedResult {
	t.Helper()
	r, err := l.Submit(context.Background(), p)
	if err != nil || r.State != "response" || !r.Receipt.HasResult || !r.Receipt.Result || l.pending == nil {
		t.Fatal("native management prematurely settled", err, r.State)
	}
	return r
}
func nativeManagedVerify(t *testing.T, l *ManagementLease) ManagedResult {
	t.Helper()
	r, err := l.Verify(context.Background())
	if err != nil || r.State != "applied" || !r.Matched || len(r.Observation.Responses) < 2 || l.pending != nil {
		t.Fatal("native original management verification", err, r.State, r.States)
	}
	return r
}

func TestQdrantNativeManagementSessionOriginalClaimsOneUseAndCompleteReadback(t *testing.T) {
	s, l := nativeManagedLease(t)
	name := "owned_bound_manage_" + fmt.Sprint(time.Now().UnixNano())
	p := nativeManagedAction(t, l, name, "collection_create")
	r := nativeManagedSubmit(t, l, p)
	if s.namespaceOwner != l || !s.claimedCollections[name+"_created"] {
		t.Fatal("original namespace was not claimed")
	}
	if _, err := s.ReserveQuery(20); !errors.Is(err, contract.ErrNamespaceBusy) {
		t.Fatal("REST document opened across unresolved management", err)
	}
	if _, err := s.ReserveDataDocument(s.Namespace(name+"_created").Object("points", "points")); !errors.Is(err, contract.ErrNamespaceBusy) {
		t.Fatal("affected point document opened across management", err)
	}
	other, err := s.ReserveDataDocument(s.Namespace(name+"_unrelated").Object("points", "points"))
	if err != nil {
		t.Fatal("unrelated original point scope was blocked", err)
	}
	other.Close()
	if _, err = l.Read(context.Background(), []string{name + "_created"}); !errors.Is(err, contract.ErrNamespaceBusy) {
		t.Fatal("new operation replaced original receipt", err)
	}
	r.Receipt.Response.Body[0] = 'x'
	verified := nativeManagedVerify(t, l)
	if !strings.Contains(string(verified.Receipt.Response.Body), `"result":true`) || s.namespaceOwner != nil {
		t.Fatal("caller changed receipt or completed claim leaked")
	}
	if _, err = l.Submit(context.Background(), p); !errors.Is(err, ErrManagementUsed) {
		t.Fatal("verified original preview replayed", err)
	}
	name += "_created"
	o := nativeManagedRead(t, l, name)
	config := []byte(`{"params":{"on_disk_payload":true},"hnsw_config":{"m":24},"metadata":{"n":9007199254740993,"old":true,"nested":{"old":true},"first_null":null}}`)
	p, err = l.PrepareCollection(o, name, "collection_update", config, 30)
	if err != nil {
		t.Fatal(err)
	}
	config[1] = 'x'
	o.Configs[name][0] = 'x'
	nativeManagedSubmit(t, l, p)
	nativeManagedVerify(t, l)
	o = nativeManagedRead(t, l, name)
	p, err = l.PrepareCollection(o, name, "collection_update", []byte(`{"metadata":{}}`), 30)
	if err != nil {
		t.Fatal(err)
	}
	nativeManagedSubmit(t, l, p)
	verified = nativeManagedVerify(t, l)
	fields, _ := object(verified.Observation.Configs[name])
	if !managementEqual(fields["metadata"], []byte(`{"n":9007199254740993,"old":true,"nested":{"old":true},"first_null":null}`)) {
		t.Fatal("first native metadata lost null key or empty PATCH changed prior keys", string(fields["metadata"]))
	}
	for _, tc := range []struct{ patch, expected string }{
		{`{"nested":{"new":true,"inside_null":null},"old":null,"first_null":null}`, `{"n":9007199254740993,"nested":{"new":true,"inside_null":null}}`},
		{`null`, `{"n":9007199254740993,"nested":{"new":true,"inside_null":null}}`},
		{`{"n":null,"nested":null}`, `{}`},
	} {
		o = nativeManagedRead(t, l, name)
		p, err = l.PrepareCollection(o, name, "collection_update", []byte(`{"metadata":`+tc.patch+`}`), 30)
		if err != nil {
			t.Fatal(err)
		}
		nativeManagedSubmit(t, l, p)
		verified = nativeManagedVerify(t, l)
		fields, _ = object(verified.Observation.Configs[name])
		if !managementEqual(fields["metadata"], []byte(tc.expected)) {
			t.Fatal("native shallow metadata merge, null deletion or optional no-op diverged", tc.patch, string(fields["metadata"]))
		}
	}
	p = nativeManagedAction(t, l, name, "aliases")
	nativeManagedSubmit(t, l, p)
	nativeManagedVerify(t, l)
	o = nativeManagedRead(t, l, name)
	p, err = l.PrepareAliases(o, []AliasChange{{Action: "rename_alias", Alias: name + "_alias", Destination: name + "_renamed"}}, 30)
	if err != nil {
		t.Fatal(err)
	}
	nativeManagedSubmit(t, l, p)
	nativeManagedVerify(t, l)
	p = nativeManagedAction(t, l, name, "collection_delete")
	nativeManagedSubmit(t, l, p)
	nativeManagedVerify(t, l)
	o = nativeManagedRead(t, l, name)
	p, err = l.PrepareCollection(o, name, "collection_create", []byte(`{"vectors":{"size":2,"distance":"Dot"},"metadata":{"created_null":null}}`), 30)
	if err != nil {
		t.Fatal(err)
	}
	nativeManagedSubmit(t, l, p)
	verified = nativeManagedVerify(t, l)
	fields, _ = object(verified.Observation.Configs[name])
	if !managementEqual(fields["metadata"], []byte(`{"created_null":null}`)) {
		t.Fatal("created metadata null key changed", string(fields["metadata"]))
	}
	t.Log("Original signed observations, immutable previews/receipts, namespace claims, independent unaffected documents, complete config/metadata/alias readback and one-use plans verified")
}

func TestQdrantNativeManagementSessionPreflightChangesActiveDocumentsAndDeniedReceipt(t *testing.T) {
	s, l := nativeManagedLease(t)
	admin := nativeOpen(t)
	name := nativeCollection(t, admin, `{"vectors":{"size":2,"distance":"Dot"},"on_disk_payload":false}`)
	p := nativeManagedAction(t, l, name, "collection_update")
	data, err := s.ReserveDataDocument(s.Namespace(name).Object("points", "points"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = l.Submit(context.Background(), p); !errors.Is(err, ErrManagementInUse) || l.pending != nil {
		t.Fatal("active original data document allowed mutation", err)
	}
	data.Close()
	query, err := s.ReserveQuery(30)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = l.Submit(context.Background(), p); !errors.Is(err, ErrManagementInUse) {
		t.Fatal("arbitrary REST lease allowed mutation", err)
	}
	query.Release()
	nativeManagement(t, admin, nativeCollectionPlan(t, name, "collection_update", `{"params":{"on_disk_payload":true}}`))
	result, err := l.Submit(context.Background(), p)
	if !errors.Is(err, contract.ErrNamespaceChanged) || result.State != "not_submitted" || l.pending != nil || s.namespaceOwner != nil {
		t.Fatal("changed original preflight dispatched", err, result.State)
	}
	o := nativeManagedRead(t, l, name)
	p, err = l.PrepareCollection(o, name, "collection_update", []byte(`{"params":{"on_disk_payload":false}}`), 30)
	if err != nil {
		t.Fatal(err)
	}
	// Unrelated remote creation does not invalidate the captured affected scope.
	nativeCollection(t, admin, `{"vectors":{"size":2,"distance":"Dot"}}`)
	nativeManagedSubmit(t, l, p)
	nativeManagedVerify(t, l)
	cfg, _, readonly := nativeFixture(t)
	if cfg.Auth == "api_key" {
		ro := testSession(t, cfg, readonly)
		rl, err := ro.ReserveManagementDocument(1)
		if err != nil {
			t.Fatal(err)
		}
		o := nativeManagedRead(t, rl, name)
		p, err := rl.PrepareCollection(o, name, "collection_update", []byte(`{"params":{"on_disk_payload":false}}`), 30)
		if err != nil {
			t.Fatal(err)
		}
		r, err := rl.Submit(context.Background(), p)
		if err == nil || r.Receipt.Response.Status != 403 || rl.pending == nil {
			t.Fatal("native denied receipt discarded", err)
		}
		verified, err := rl.Verify(context.Background())
		if err != nil || verified.State != "indeterminate" || !verified.Matched || verified.Receipt.HasResult || rl.pending == nil {
			t.Fatal("matching unchanged data promoted denied request", err, verified.State)
		}
	}
}

func TestQdrantNativeManagementSessionLostCanceledLeaseAndSessionCloseRetainOriginalOutcome(t *testing.T) {
	for _, action := range []string{"collection_create", "collection_update", "collection_delete", "aliases"} {
		for _, fault := range []string{"lost", "canceled", "lease_close", "session_close"} {
			t.Run(action+"/"+fault, func(t *testing.T) {
				upstream := nativeOpen(t)
				name := nativeCollection(t, upstream, `{"vectors":{"size":2,"distance":"Dot"},"on_disk_payload":false}`)
				observed, acked := make(chan struct{}), make(chan struct{})
				var writes atomic.Int32
				cfg := nativeDataWriteRelay(t, upstream, func(w http.ResponseWriter, r *http.Request, response Response) bool {
					if r.Method == "GET" {
						return false
					}
					writes.Add(1)
					close(observed)
					if fault == "lost" {
						conn, _, err := w.(http.Hijacker).Hijack()
						if err != nil {
							t.Error(err)
						} else {
							conn.Close()
						}
					} else {
						<-r.Context().Done()
					}
					close(acked)
					return true
				}, nil)
				s := testSession(t, cfg, "")
				l, err := s.ReserveManagementDocument(1)
				if err != nil {
					t.Fatal(err)
				}
				p := nativeManagedAction(t, l, name, action)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				type outcome struct {
					r   ManagedResult
					err error
				}
				out := make(chan outcome, 1)
				go func() { r, e := l.Submit(ctx, p); out <- outcome{r, e} }()
				waitSessionSignal(t, observed)
				originalConfig, originalKey, _ := nativeFixture(t)
				fresh := testSession(t, originalConfig, originalKey)
				q, err := fresh.ReserveQuery(100)
				if err != nil {
					t.Fatal(err)
				}
				defer q.Release()
				if err = q.Preconnect(context.Background()); err != nil {
					t.Fatal(err)
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
				var got outcome
				select {
				case got = <-out:
				case <-time.After(10 * time.Second):
					t.Fatal("original management executor did not acknowledge close/cancel")
				}
				waitSessionSignal(t, acked)
				var interrupted *Interrupted
				if !errors.As(got.err, &interrupted) || !interrupted.WriteOutcomeUnknown || got.r.State != "uncertain" || got.r.Receipt.HasResult {
					t.Fatal("original unknown native mutation became settled", got.err, got.r.State)
				}
				if _, err = q.Execute(context.Background(), "", "GET /collections"); err != nil {
					t.Fatal("original close retired another captured session", err)
				}
				if fault == "lost" || fault == "canceled" {
					r, err := l.Verify(context.Background())
					if err != nil || r.State != "indeterminate" || !r.Matched || l.pending == nil {
						t.Fatal("observed effect invented original boolean receipt", err, r.State, r.States)
					}
					if _, err = l.Submit(context.Background(), p); !errors.Is(err, contract.ErrNamespaceBusy) {
						t.Fatal("unknown management plan replayed", err)
					}
				} else if _, err = l.Verify(context.Background()); !errors.Is(err, contract.ErrRevoked) {
					t.Fatal("closed original lease recovered through another session", err)
				}
				if writes.Load() != 1 {
					t.Fatal("management mutation repeated")
				}
				if action == "collection_create" {
					if _, _, err = upstream.Collection(context.Background(), name+"_created"); err != nil {
						t.Fatal("native creation effect lost", err)
					}
				}
				if action == "collection_delete" {
					if _, response, err := upstream.Collection(context.Background(), name); err == nil || response.Status != 404 {
						t.Fatal("native deletion effect lost", err)
					}
				}
				if action == "collection_update" {
					info, _, err := upstream.Collection(context.Background(), name)
					if err != nil || !strings.Contains(string(info.Result), `"on_disk_payload":true`) {
						t.Fatal("native update effect lost", err)
					}
				}
				if action == "aliases" {
					aliases, _, err := upstream.Aliases(context.Background())
					found := false
					for _, a := range aliases {
						found = found || a == (Alias{Alias: name + "_alias", Collection: name})
					}
					if err != nil || !found {
						t.Fatal("native alias effect lost", err)
					}
				}
			})
		}
	}
}

func TestQdrantNativeManagementSessionReadbackInterruptionRetainsReceiptAndNeverMutates(t *testing.T) {
	for _, fault := range []string{"lost", "canceled"} {
		t.Run(fault, func(t *testing.T) {
			upstream := nativeOpen(t)
			name := nativeCollection(t, upstream, `{"vectors":{"size":2,"distance":"Dot"},"on_disk_payload":false}`)
			var enabled atomic.Bool
			var writes atomic.Int32
			observed, acked := make(chan struct{}), make(chan struct{})
			cfg := nativeDataWriteRelay(t, upstream, func(w http.ResponseWriter, r *http.Request, response Response) bool {
				if r.Method != "GET" {
					writes.Add(1)
				}
				if r.Method != "GET" || !strings.HasSuffix(r.URL.Path, "/collections/"+name) || !enabled.CompareAndSwap(true, false) {
					return false
				}
				close(observed)
				if fault == "lost" {
					conn, _, err := w.(http.Hijacker).Hijack()
					if err != nil {
						t.Error(err)
					} else {
						conn.Close()
					}
				} else {
					<-r.Context().Done()
				}
				close(acked)
				return true
			}, nil)
			s := testSession(t, cfg, "")
			l, err := s.ReserveManagementDocument(1)
			if err != nil {
				t.Fatal(err)
			}
			p := nativeManagedAction(t, l, name, "collection_update")
			submitted := nativeManagedSubmit(t, l, p)
			enabled.Store(true)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			type outcome struct {
				r   ManagedResult
				err error
			}
			out := make(chan outcome, 1)
			go func() { r, e := l.Verify(ctx); out <- outcome{r, e} }()
			waitSessionSignal(t, observed)
			if fault == "canceled" {
				cancel()
			}
			var got outcome
			select {
			case got = <-out:
			case <-time.After(10 * time.Second):
				t.Fatal("original readback did not settle")
			}
			waitSessionSignal(t, acked)
			var interrupted *Interrupted
			if !errors.As(got.err, &interrupted) || interrupted.WriteOutcomeUnknown || got.r.State != "indeterminate" || !got.r.Receipt.HasResult || string(got.r.Receipt.Response.Body) != string(submitted.Receipt.Response.Body) || l.pending == nil {
				t.Fatal("readback interruption changed original mutation receipt", got.err, got.r.State)
			}
			if len(got.r.Observation.Responses) < 2 || len(l.pending.last.Observation.Responses) < 2 {
				t.Fatal("partial original read evidence discarded")
			}
			nativeManagedVerify(t, l)
			if writes.Load() != 1 {
				t.Fatal("verification replayed native mutation")
			}
		})
	}
}
