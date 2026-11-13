package consul

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

func TestAgentServiceOriginalDefinitionAndPreparedReplacementNeverAliasOrDispatch(t *testing.T) {
	original := []byte(" {\n\"Name\":\"original\",\"ID\":null,\"Tags\":[],\"Meta\":{\"Namespace\":\"user metadata\"},\"Checks\":[],\"UnknownNative\":{\"Exact\":18446744073709551615},\"Connect\":{\"SidecarService\":{\"Port\":9000}}\n} ")
	disabled, enabled := false, true
	for _, replacement := range []*bool{nil, &disabled, &enabled} {
		var calls atomic.Int32
		c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			raw, _ := io.ReadAll(r.Body)
			if !bytes.Equal(raw, original) || r.URL.Query().Has("dc") || r.URL.Query().Has("consistent") || r.URL.Query().Has("stale") || r.URL.Query().Has("replace-existing-checks") != (replacement != nil) {
				t.Error("original definition/replacement/local agent scope changed")
			}
			if replacement != nil && r.URL.Query().Get("replace-existing-checks") != fmt.Sprint(*replacement) {
				t.Error("explicit native replacement changed")
			}
			w.Header().Set("X-Original-Receipt", "retained")
			w.WriteHeader(200)
		})
		c.config.Datacenter, c.config.Prefix, c.config.Consistency = "foreign", "foreign/", "consistent"
		raw := bytes.Clone(original)
		r, err := AgentServiceRegisterRequest(raw, replacement)
		if err != nil {
			t.Fatal(err)
		}
		p, err := c.Prepare(r)
		if err != nil || !p.Writes() || calls.Load() != 0 {
			t.Fatal("registration prepare performed I/O or did not capture a write", err)
		}
		raw[1], r.Body[2] = 'x', 'x'
		r.Query = url.Values{"replace-existing-checks": {"changed"}}
		inspection := p.Snapshot()
		inspection.Body[3] = 'x'
		if inspection.Query == nil {
			inspection.Query = url.Values{}
		}
		inspection.Query.Set("replace-existing-checks", "changed")
		result, err := c.Execute(context.Background(), p)
		if err != nil || !result.Acknowledged || !result.Succeeded || !result.Captured || !result.EOF || result.WriteOutcomeUnknown || len(result.Body) != 0 || !bytes.Equal(result.Request.Body, original) {
			t.Fatal("empty original Agent ACK changed", err)
		}
		if _, err := c.Execute(context.Background(), p); !errors.Is(err, ErrPlanUsed) || calls.Load() != 1 {
			t.Fatal("original Agent registration replayed", err)
		}
		result.Request.Body[0] = 'x'
		result.Header.Set("X-Original-Receipt", "changed")
		if !bytes.Equal(c.LastResponse().Request.Body, original) || c.LastResponse().Header.Get("X-Original-Receipt") != "retained" {
			t.Fatal("receipt inspection changed original definition")
		}
	}
}

func TestAgentServiceDefinitionScopeAndNativeIDRejectRewritingBeforeIO(t *testing.T) {
	for _, raw := range []string{`null`, `[]`, `{}`, `{"Name":null}`, `{"Name":""}`, `{"Name":"x","name":"y"}`, `{"Name":"x","Name":"x"}`, `{"Name":"x","Namespace":""}`, `{"Name":"x","partition":null}`, `{"Name":"x","Connect":{"SidecarService":{"Token":"embedded-secret"}}}`, `{"Name":"x","Proxy":{"Upstreams":[{"Namespace":"foreign"}]}}`} {
		if _, err := AgentServiceRegisterRequest([]byte(raw), nil); err == nil {
			t.Fatal("invalid or unverified native definition accepted")
		}
	}
	for _, id := range []string{"", "\x00", "line\nfeed", strings.Repeat("x", 4097), "a//b", "a/../b", "a/./b", "/leading", "trailing/"} {
		if _, err := AgentServiceDeregisterRequest(id); err == nil {
			t.Fatal("unsafe original ID normalized instead of rejected")
		}
	}
	for _, id := range []string{"service-ID", "Unicode 雪", "literal%2F", "literal?#++", "qualified/id"} {
		r, err := AgentServiceDeregisterRequest(id)
		if err != nil || r.Path != "/v1/agent/service/deregister/"+url.PathEscape(id) || r.Method != http.MethodPut || r.Body != nil {
			t.Fatal("native string ID changed", err)
		}
		for _, query := range []url.Values{{"dc": {"foreign"}}, {"partition": {""}}, {"index": {"0"}}, {"filter": {""}}, {"replace-existing-checks": {"true"}}} {
			r.Query = query
			if _, _, _, err := validateRequest(r); err == nil {
				t.Fatal("deregistration accepted foreign native options")
			}
		}
	}
	r, err := AgentServiceRegisterRequest([]byte(`{"Name":"original","ID":"id"}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, query := range []url.Values{{"index": {"0"}}, {"dc": {"foreign"}}, {"replace-existing-checks": {"1"}}, {"replace-existing-checks": {"TRUE"}}, {"replace-existing-checks": {"true", "false"}}} {
		r.Query = query
		if _, _, _, err := validateRequest(r); err == nil {
			t.Fatal("invalid original replacement/scope accepted")
		}
	}
	r.Query = url.Values{"replace-existing-checks": {""}}
	if _, _, _, err := validateRequest(r); err != nil {
		t.Fatal("explicit empty native replacement lost", err)
	}
}

func TestAgentServiceFailuresKeepOriginalUnknownSeparateFromLaterAgentReads(t *testing.T) {
	for _, kind := range []string{"register", "deregister"} {
		for _, reply := range []struct {
			status int
			body   string
		}{{400, "original partial registration error"}, {401, "original auth error"}, {409, "original conflict"}, {422, "original validation error"}, {500, "original processed error"}, {408, "original processed timeout"}, {200, "true"}, {200, "null"}, {200, " "}, {200, "{}"}} {
			t.Run(fmt.Sprintf("%s/%d/%s", kind, reply.status, reply.body), func(t *testing.T) {
				var calls atomic.Int32
				c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					if r.Method == http.MethodGet {
						w.Write([]byte(`{"original":"read"}`))
						return
					}
					w.WriteHeader(reply.status)
					w.Write([]byte(reply.body))
				})
				r, err := AgentServiceRegisterRequest([]byte(`{"Name":"original","ID":"id"}`), nil)
				if kind == "deregister" {
					r, err = AgentServiceDeregisterRequest("id")
				}
				if err != nil {
					t.Fatal(err)
				}
				prepared, _ := c.Prepare(r)
				later, _ := c.Prepare(putRequest("later", nil))
				original, err := c.Execute(context.Background(), prepared)
				if err == nil || original.Acknowledged || original.Succeeded || !original.WriteOutcomeUnknown || !original.Captured || !original.EOF || !c.Alive() {
					t.Fatal("native failure invented no-effect acknowledgement", err)
				}
				before := original.Clone()
				if _, err := c.Execute(context.Background(), later); !errors.Is(err, ErrWriteUnknown) || calls.Load() != 1 {
					t.Fatal("earlier prepared write bypassed original barrier", err)
				}
				if _, err := c.Do(context.Background(), AgentSelfRequest()); err != nil {
					t.Fatal(err)
				}
				unknown, ok := c.UnknownResponse()
				if !ok || !reflect.DeepEqual(unknown, before) || calls.Load() != 2 {
					t.Fatal("later successful Agent read replaced original uncertain write")
				}
			})
		}
	}
}

func TestAgentServiceProvedNativeACLAndMissingIDDenyWithoutInventingUnknown(t *testing.T) {
	for _, kind := range []string{"register", "deregister"} {
		for _, status := range []int{403, 404} {
			if kind == "register" && status == 404 {
				continue
			}
			c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(status)
				w.Write([]byte("original denial"))
			})
			r, err := AgentServiceRegisterRequest([]byte(`{"Name":"original"}`), nil)
			if kind == "deregister" {
				r, err = AgentServiceDeregisterRequest("id")
			}
			if err != nil {
				t.Fatal(err)
			}
			result, err := c.Do(context.Background(), r)
			if err == nil || !result.Acknowledged || result.Succeeded || result.WriteOutcomeUnknown || !result.Captured || !c.Alive() {
				t.Fatal("proved native denial changed", err)
			}
			if _, err := c.Prepare(putRequest("later", nil)); err != nil {
				t.Fatal("known denial invented a write barrier", err)
			}
		}
	}
}
