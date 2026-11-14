package consul

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"sync/atomic"
	"testing"
	"time"
)

// Only the fixture relay sees the complete upstream secret response. The
// request is actually processed before any loss, replacement or cancellation.
func nativeACLMutationFault(t *testing.T, cfg Config, secret, mode string, request Request) (Config, *atomic.Int32, <-chan []byte) {
	t.Helper()
	upstream, err := New(cfg, secret)
	if err != nil {
		t.Fatal("owned ACL relay transport unavailable")
	}
	t.Cleanup(func() { upstream.Close() })
	writes := &atomic.Int32{}
	processed := make(chan []byte, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, incoming *http.Request) {
		raw, err := io.ReadAll(incoming.Body)
		if err != nil {
			return
		}
		forward, err := http.NewRequestWithContext(incoming.Context(), incoming.Method, cfg.Endpoint+incoming.URL.RequestURI(), bytes.NewReader(raw))
		if err != nil {
			t.Error("owned ACL forwarding request failed")
			return
		}
		forward.Header, forward.GetBody, forward.Close = incoming.Header.Clone(), nil, true
		response, err := upstream.http.Do(forward)
		if err != nil {
			return
		}
		body, err := io.ReadAll(response.Body)
		response.Body.Close()
		if err != nil {
			return
		}
		if incoming.Method == request.Method && incoming.URL.EscapedPath() == request.Path {
			if writes.Add(1) != 1 || response.StatusCode != 200 || !json.Valid(body) {
				t.Error("relay did not capture one complete native ACL mutation")
				return
			}
			processed <- bytes.Clone(body)
			switch mode {
			case "lost", "partial":
				connection, output, err := w.(http.Hijacker).Hijack()
				if err == nil {
					defer connection.Close()
					if mode == "partial" {
						fmt.Fprintf(output, "HTTP/1.1 200 OK\r\nContent-Length: %d\r\nX-Original: retained\r\n\r\n", len(body)+17)
						output.Write(body[:len(body)/2])
						output.Flush()
					}
				}
				return
			case "http400", "http500", "http408":
				w.WriteHeader(map[string]int{"http400": 400, "http500": 500, "http408": 408}[mode])
				io.WriteString(w, "original processed ACL reply withheld")
				return
			case "invalid":
				io.WriteString(w, "null")
				return
			case "cancel", "close":
				<-incoming.Context().Done()
				return
			}
		}
		for key, values := range response.Header {
			for _, value := range values {
				w.Header().Add(key, value)
			}
		}
		w.WriteHeader(response.StatusCode)
		w.Write(body)
	}))
	t.Cleanup(server.Close)
	relay := cfg
	relay.Endpoint = server.URL
	relay.CAFile, relay.ClientCertFile, relay.ClientKeyFile, relay.ServerName = "", "", "", ""
	return relay, writes, processed
}

func TestConsulNativeACLMutationProcessedRepliesRetainUnknownAndNeverReplay(t *testing.T) {
	base, cfg, secret := nativeClient(t)
	build := nativeCatalogRequest(t)
	if cfg.Auth != "token" {
		nativeACLMutation(t, base, build(ACLCreateRequest(ACLReadToken, []byte(`{}`))), 401)
		return
	}
	for _, action := range []string{"create", "update", "delete", "clone"} {
		for _, mode := range []string{"lost", "partial", "http400", "http500", "http408", "invalid", "cancel", "close"} {
			t.Run(action+"/"+mode, func(t *testing.T) {
				resource := ACLReadToken
				var sourceID string
				if action == "update" {
					resource = ACLReadPolicy
				} else if action == "delete" {
					resource = ACLReadRole
				}
				name := "owned-acl-" + action + "-" + mode
				removeSource := action != "create"
				if removeSource {
					fields := map[string]any{"Description": "owned original"}
					identityField := "AccessorID"
					if resource != ACLReadToken {
						fields["Name"], identityField = name, "ID"
					}
					raw := nativeACLFixtureCall(t, base, "PUT", "/v1/acl/"+string(resource), fields)
					sourceID = nativeACLString(t, raw, identityField)
					t.Cleanup(func() {
						if removeSource {
							nativeACLFixtureCall(t, base, "DELETE", "/v1/acl/"+string(resource)+"/"+url.PathEscape(sourceID), nil)
						}
					})
				}
				var request Request
				switch action {
				case "create":
					request = build(ACLCreateRequest(resource, []byte(`{"Description":"owned actual create"}`)))
				case "update":
					raw, _ := json.Marshal(map[string]string{"Name": name, "Description": "owned actual update", "Rules": `acl = "read"`})
					request = build(ACLUpdateRequest(resource, sourceID, raw))
				case "delete":
					request = build(ACLDeleteRequest(resource, sourceID))
				case "clone":
					request = build(ACLTokenCloneRequest(sourceID, new("owned actual clone")))
				}
				relay, writes, processed := nativeACLMutationFault(t, cfg, secret, mode, request)
				client, err := New(relay, secret)
				if err != nil {
					t.Fatal("owned original ACL client unavailable")
				}
				t.Cleanup(func() { client.Close() })
				plan, err := client.Prepare(request)
				if err != nil {
					t.Fatal("original ACL fault plan unavailable")
				}
				pending, err := client.Prepare(request)
				if err != nil {
					t.Fatal("original second preparation unavailable")
				}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				type completion struct {
					result Response
					err    error
				}
				done := make(chan completion, 1)
				go func() {
					r, err := client.Execute(ctx, plan)
					done <- completion{r, err}
				}()
				var observed []byte
				select {
				case observed = <-processed:
				case <-time.After(20 * time.Second):
					t.Fatal("original native ACL write was not processed")
				}
				identity := sourceID
				if action == "create" || action == "clone" {
					identity = nativeACLString(t, observed, "AccessorID")
					t.Cleanup(func() { nativeACLFixtureCall(t, base, "DELETE", "/v1/acl/token/"+identity, nil) })
				} else if action == "delete" {
					removeSource = false
				}
				closed := make(chan struct{})
				if mode == "cancel" {
					cancel()
				} else if mode == "close" {
					go func() { client.Close(); close(closed) }()
				}
				var out completion
				select {
				case out = <-done:
				case <-time.After(20 * time.Second):
					t.Fatal("original ACL executor did not acknowledge cancellation or loss")
				}
				if mode == "close" {
					wait(t, closed)
				}
				original := out.result.Clone()
				if out.err == nil || !original.Attempted || !original.Ended || original.Acknowledged || original.Succeeded || !original.WriteOutcomeUnknown || writes.Load() != 1 || !reflect.DeepEqual(original.Request, request) {
					t.Fatal("processed original ACL write was guessed or replayed")
				}
				if mode == "partial" && (original.Captured || original.EOF || !bytes.Equal(original.Body, observed[:len(observed)/2]) || original.Header.Get("X-Original") != "retained") {
					t.Fatal("original partial ACL bytes lost their incomplete boundary")
				}
				if mode == "http400" || mode == "http500" || mode == "http408" || mode == "invalid" {
					if !original.Captured || !original.EOF {
						t.Fatal("complete failed ACL response became a transport failure")
					}
					if _, err := client.Execute(context.Background(), pending); !errors.Is(err, ErrWriteUnknown) {
						t.Fatal("prepared ACL write bypassed original unknown barrier")
					}
					read := build(ACLListReadRequest(resource))
					nativeACLRead(t, client, read, 200)
					unknown, exists := client.UnknownResponse()
					if !exists || !reflect.DeepEqual(unknown, original) {
						t.Fatal("later complete ACL inventory replaced original unknown write")
					}
					for _, next := range []Request{request, putRequest("owned-acl-later", nil)} {
						if _, err := client.Prepare(next); !errors.Is(err, ErrWriteUnknown) || writes.Load() != 1 {
							t.Fatal("later read removed the cross-operation write barrier")
						}
					}
				} else {
					if client.Alive() {
						t.Fatal("lost or canceled ACL executor remained reusable")
					}
					if _, err := client.Execute(context.Background(), pending); !errors.Is(err, ErrRetired) || writes.Load() != 1 {
						t.Fatal("retired ACL executor replayed an earlier preparation")
					}
					if mode != "close" {
						unknown, exists := client.UnknownResponse()
						if !exists || !reflect.DeepEqual(unknown, original) {
							t.Fatal("retired ACL executor lost original unknown evidence")
						}
					}
				}
				read := build(ACLItemReadRequest(resource, identity))
				if action == "delete" {
					nativeACLRead(t, base, read, 404)
				} else {
					actual := nativeACLRead(t, base, read, 200)
					if resource == ACLReadToken && nativeACLString(t, actual.Body, "SecretID") != nativeACLString(t, observed, "SecretID") {
						t.Fatal("actual generated token differs from original upstream receipt")
					}
					if nativeACLString(t, actual.Body, "Description") != "owned actual "+action {
						t.Fatal("relay fault did not follow actual original ACL mutation")
					}
				}
				if writes.Load() != 1 {
					t.Fatal("original ACL request was replayed")
				}
			})
		}
	}
}
