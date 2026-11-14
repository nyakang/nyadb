package consul

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func nativeACLDocumentSession(t *testing.T, cfg Config, token string) (*Session, *documentLease) {
	t.Helper()
	s, err := OpenNativeSession(context.Background(), 9401, cfg, token)
	if err != nil {
		t.Fatal("original ACL document session unavailable")
	}
	t.Cleanup(func() { s.Close() })
	return s, aclDocumentLease(t, s, 1)
}

func nativeACLDocumentRead(t *testing.T, l *documentLease, request contract.KVHTTPACLReadRequest, status int) contract.KVHTTPACLReadResult {
	t.Helper()
	p, err := l.PrepareHTTPACLRead(request)
	if err != nil {
		t.Fatal("original ACL document preparation failed")
	}
	r, err := l.ExecuteHTTPACLRead(context.Background(), p)
	if (err == nil) != (status == 200) || r.Receipt.HTTP == nil || r.Receipt.HTTP.Status != status || !r.Receipt.Completed || !r.Receipt.HTTP.Captured || !r.Receipt.HTTP.EOF || r.Receipt.WriteOutcomeUnknown || r.LocalRecordID != p.LocalRecordID() || !reflect.DeepEqual(r.Request, request) || !reflect.DeepEqual(r, l.LastHTTPACLRead()) || l.LastResult().HTTP != nil {
		t.Fatal("original private ACL receipt classification, identity or cache changed", status)
	}
	return r
}

func TestConsulNativeACLDocumentOriginalShapesAndPrivateRecovery(t *testing.T) {
	base, cfg, token := nativeClient(t)
	s, l := nativeACLDocumentSession(t, cfg, token)
	resources := []ACLReadResource{ACLReadToken, ACLReadPolicy, ACLReadRole, ACLReadAuthMethod, ACLReadBindingRule, ACLReadTemplatedPolicy}
	if cfg.Auth != "token" {
		for _, resource := range resources {
			r := nativeACLDocumentRead(t, l, contract.KVHTTPACLReadRequest{Resource: string(resource), Lookup: "list"}, 401)
			if r.Projection != nil {
				t.Fatal("ACL-disabled rejection became an empty inventory")
			}
		}
		return
	}
	f := nativeACLSeed(t, base)
	for _, resource := range resources {
		request := contract.KVHTTPACLReadRequest{Resource: string(resource), Lookup: "list"}
		if resource != ACLReadTemplatedPolicy {
			request.Index, request.Wait = new(uint64(0)), new("1ms")
		}
		r := nativeACLDocumentRead(t, l, request, 200)
		if r.Projection == nil || !r.Projection.ProjectionComplete || !r.Projection.Complete || len(r.Projection.Rows) == 0 {
			t.Fatal("native ACL list shape unavailable", resource)
		}
	}
	for _, item := range []struct{ resource, lookup, identity string }{
		{"token", "id", f.accessor}, {"policy", "id", f.policy}, {"policy", "name", f.policyName},
		{"role", "id", f.role}, {"role", "name", f.roleName}, {"auth-method", "name", f.method},
		{"binding-rule", "id", f.binding}, {"templated-policy", "name", "builtin/service"},
	} {
		r := nativeACLDocumentRead(t, l, contract.KVHTTPACLReadRequest{Resource: item.resource, Lookup: item.lookup, Identity: item.identity}, 200)
		if r.Projection == nil || len(r.Projection.Rows) != 1 || !r.Projection.Complete || !bytes.Equal(bytes.TrimSpace(r.Receipt.HTTP.Body), r.Projection.Rows[0].Raw) {
			t.Fatal("native ACL detail lost complete original fields", item.resource)
		}
	}
	original := nativeACLDocumentRead(t, l, contract.KVHTTPACLReadRequest{Resource: "token", Lookup: "id", Identity: f.linked, Expanded: true}, 200)
	if original.Projection == nil || original.Projection.Rows[0].Identity != f.linked || !bytes.Contains(original.Projection.Rows[0].Raw, []byte(`"ExpandedPolicies"`)) {
		t.Fatal("expanded native token lost original details")
	}
	r, err := s.RecoverKV(l)
	if err != nil || r.ACLResult == nil || !reflect.DeepEqual(*r.ACLResult, original) || r.Last.HTTP != nil || r.Unknown != nil || !reflect.DeepEqual(r.Lease.(contract.KVHTTPACLReading).LastHTTPACLRead(), original) || l.LastHTTPACLRead().Receipt.HTTP != nil {
		t.Fatal("native ACL recovery mixed or discarded original private receipt")
	}
	t.Cleanup(func() { r.Lease.Close() })
}

func TestConsulNativeACLDocumentSelfRedactionDenialAndAbsence(t *testing.T) {
	base, cfg, token := nativeClient(t)
	if cfg.Auth != "token" {
		_, l := nativeACLDocumentSession(t, cfg, token)
		nativeACLDocumentRead(t, l, contract.KVHTTPACLReadRequest{Resource: "token", Lookup: "self"}, 401)
		return
	}
	f := nativeACLSeed(t, base)
	_, reader := nativeACLDocumentSession(t, cfg, f.readonly)
	self := nativeACLDocumentRead(t, reader, contract.KVHTTPACLReadRequest{Resource: "token", Lookup: "self"}, 200)
	if self.Projection == nil || self.Projection.Rows[0].Identity != f.accessor || self.Projection.Rows[0].SecretState != "value" || !self.Projection.ContainsSensitiveValues || nativeACLString(t, self.Receipt.HTTP.Body, "SecretID") != f.readonly {
		t.Fatal("native token self did not preserve private original credential")
	}
	detail := nativeACLDocumentRead(t, reader, contract.KVHTTPACLReadRequest{Resource: "token", Lookup: "id", Identity: f.accessor}, 200)
	if detail.Projection == nil || !detail.Projection.ContainsRedaction || detail.Projection.Rows[0].SecretState != "redacted" || nativeACLString(t, detail.Receipt.HTTP.Body, "SecretID") != "<hidden>" {
		t.Fatal("native token redaction lost")
	}
	_, admin := nativeACLDocumentSession(t, cfg, token)
	config := nativeACLDocumentRead(t, admin, contract.KVHTTPACLReadRequest{Resource: "auth-method", Lookup: "name", Identity: f.method}, 200)
	if config.Projection == nil || !config.Projection.ContainsSensitiveValues || config.Projection.Rows[0].ConfigState != "value" {
		t.Fatal("original AuthMethod Config escaped private classification")
	}
	_, denied := nativeACLDocumentSession(t, cfg, f.denied)
	if r := nativeACLDocumentRead(t, denied, contract.KVHTTPACLReadRequest{Resource: "policy", Lookup: "list"}, 403); r.Projection != nil {
		t.Fatal("denied original ACL response became an empty inventory")
	}
	if r := nativeACLDocumentRead(t, admin, contract.KVHTTPACLReadRequest{Resource: "policy", Lookup: "id", Identity: "00000000-0000-0000-0000-000000000099"}, 404); r.Projection != nil {
		t.Fatal("missing native ACL policy became a successful detail")
	}
}

func nativeACLDocumentRelay(t *testing.T, cfg Config, token, mode string) (Config, *atomic.Int32, <-chan struct{}) {
	t.Helper()
	up, err := New(cfg, token)
	if err != nil {
		t.Fatal("original ACL relay unavailable")
	}
	t.Cleanup(func() { up.Close() })
	calls := new(atomic.Int32)
	processed := make(chan struct{})
	var once atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, incoming *http.Request) {
		request, err := http.NewRequestWithContext(incoming.Context(), incoming.Method, cfg.Endpoint+incoming.URL.RequestURI(), incoming.Body)
		if err != nil {
			t.Error("original ACL relay request unavailable")
			return
		}
		request.Header, request.GetBody, request.Close = incoming.Header.Clone(), nil, true
		response, err := up.http.Do(request)
		if err != nil {
			return
		}
		body, err := io.ReadAll(response.Body)
		response.Body.Close()
		if err != nil {
			t.Error("original ACL relay response incomplete")
			return
		}
		for key, values := range response.Header {
			for _, value := range values {
				w.Header().Add(key, value)
			}
		}
		if incoming.URL.Path == "/v1/acl/token/self" {
			calls.Add(1)
			if once.CompareAndSwap(false, true) {
				if response.StatusCode != 200 {
					t.Error("original ACL relay read denied")
					return
				}
				close(processed)
				switch mode {
				case "lost":
					connection, _, err := w.(http.Hijacker).Hijack()
					if err == nil {
						connection.Close()
					}
					return
				case "partial":
					w.Header().Set("Content-Length", strconv.Itoa(len(body)+1))
					w.WriteHeader(200)
					w.Write(body)
					return
				case "cancel", "close", "retire", "recover":
					<-incoming.Context().Done()
					return
				}
			}
		}
		w.WriteHeader(response.StatusCode)
		w.Write(body)
	}))
	t.Cleanup(server.Close)
	proxy := cfg
	proxy.Endpoint = server.URL
	proxy.CAFile, proxy.ServerName, proxy.ClientCertFile, proxy.ClientKeyFile = "", "", "", ""
	return proxy, calls, processed
}

func TestConsulNativeACLDocumentFaultsCancelCloseAndRecoveryNeverReplay(t *testing.T) {
	_, cfg, token := nativeClient(t)
	if cfg.Auth != "token" {
		_, l := nativeACLDocumentSession(t, cfg, token)
		nativeACLDocumentRead(t, l, contract.KVHTTPACLReadRequest{Resource: "token", Lookup: "self"}, 401)
		return
	}
	for _, mode := range []string{"lost", "partial", "cancel", "close", "retire", "recover"} {
		t.Run(mode, func(t *testing.T) {
			proxy, calls, processed := nativeACLDocumentRelay(t, cfg, token, mode)
			s, l := nativeACLDocumentSession(t, proxy, token)
			p, err := l.PrepareHTTPACLRead(contract.KVHTTPACLReadRequest{Resource: "token", Lookup: "self"})
			if err != nil || calls.Load() != 0 {
				t.Fatal("native ACL preparation dispatched")
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			type completion struct {
				result contract.KVHTTPACLReadResult
				err    error
			}
			done := make(chan completion, 1)
			go func() { r, err := l.ExecuteHTTPACLRead(ctx, p); done <- completion{r, err} }()
			select {
			case <-processed:
			case <-time.After(30 * time.Second):
				t.Fatal("original ACL GET was not processed")
			}
			var recovery contract.KVRecovery
			switch mode {
			case "cancel":
				cancel()
			case "close":
				l.Close()
			case "retire":
				s.Close()
			case "recover":
				recovery, err = s.RecoverKV(l)
				if err != nil {
					t.Fatal("ACL cancellation recovery failed")
				}
				t.Cleanup(func() { recovery.Lease.Close() })
			}
			select {
			case out := <-done:
				r := out.result
				if out.err == nil || r.Receipt.HTTP == nil || !r.Receipt.Attempted || !r.Receipt.HTTP.Ended || r.Receipt.Completed || r.Receipt.WriteOutcomeUnknown || r.Projection != nil || calls.Load() != 1 || l.LastResult().HTTP != nil {
					t.Fatal("faulted ACL read promoted, disclosed or replayed")
				}
				if mode == "partial" && len(r.Receipt.HTTP.Body) == 0 || mode == "cancel" && !r.Receipt.HTTP.Canceled {
					t.Fatal("partial or canceled ACL evidence discarded")
				}
				if mode == "close" || mode == "recover" {
					if l.LastHTTPACLRead().Receipt.HTTP != nil {
						t.Fatal("old ACL cache not released")
					}
				} else if !reflect.DeepEqual(l.LastHTTPACLRead(), r) {
					t.Fatal("parked private ACL receipt lost")
				}
				if mode == "recover" && (recovery.ACLResult == nil || !reflect.DeepEqual(*recovery.ACLResult, r)) {
					t.Fatal("original canceled ACL evidence changed during recovery")
				}
			case <-time.After(5 * time.Second):
				t.Fatal("original ACL executor did not acknowledge cancellation")
			}
			if _, err := l.ExecuteHTTPACLRead(context.Background(), p); err == nil || calls.Load() != 1 {
				t.Fatal("retired ACL authority replayed")
			}
		})
	}
}

func TestConsulNativeACLDocumentReadKeepsProcessedUnknownWriteIndependent(t *testing.T) {
	base, cfg, token := nativeClient(t)
	proxy, calls, _ := nativeFault(t, cfg, token, "http500")
	s, l := nativeACLDocumentSession(t, proxy, token)
	key, value := keyFor(t, "original-acl-unknown"), []byte{0, 255, 128}
	plan := documentPlan(t, l, putRequest(key, value))
	unknown, err := l.Execute(context.Background(), plan)
	if err == nil || !unknown.WriteOutcomeUnknown || !bytes.Equal(nativeRead(t, base, key).Value, value) {
		t.Fatal("fixture did not preserve actual processed unknown write")
	}
	status := 200
	if cfg.Auth != "token" {
		status = 401
	}
	p, err := l.PrepareHTTPACLRead(contract.KVHTTPACLReadRequest{Resource: "token", Lookup: "self"})
	if err != nil {
		t.Fatal("unknown write incorrectly blocked ACL read")
	}
	acl, err := l.ExecuteHTTPACLRead(context.Background(), p)
	if (err == nil) != (status == 200) || acl.Receipt.HTTP == nil || acl.Receipt.HTTP.Status != status || !reflect.DeepEqual(l.LastResult(), unknown) || !reflect.DeepEqual(l.recoveryOwned().Unknown, &unknown) {
		t.Fatal("ACL read replaced independent unknown-write receipt")
	}
	read := executeDocument(t, l, readRequest(key))
	before := calls.Load()
	recovery, err := s.RecoverKV(l)
	if err != nil || recovery.ACLResult == nil || !reflect.DeepEqual(*recovery.ACLResult, acl) || !reflect.DeepEqual(recovery.Last, read) || !reflect.DeepEqual(recovery.Unknown, &unknown) || calls.Load() != before {
		t.Fatal("ACL recovery replayed or mixed original receipts")
	}
	t.Cleanup(func() { recovery.Lease.Close() })
	request, _ := DocumentRequest(putRequest(key, value))
	if _, err := recovery.Lease.Prepare(request); !errors.Is(err, contract.ErrKVWriteUnknown) || calls.Load() != before {
		t.Fatal("ACL read/recovery cleared original write barrier")
	}
}
