package consul

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

func TestAgentLocalReadsCaptureFilterPresenceAndRejectForeignScope(t *testing.T) {
	for _, build := range []func(*string) (Request, error){AgentServicesRequest, AgentChecksRequest} {
		for _, filter := range []*string{nil, str(""), str("Service == `original 中文`")} {
			r, err := build(filter)
			if err != nil || r.Query.Has("filter") != (filter != nil) {
				t.Fatal("original filter presence changed", err)
			}
			if filter != nil {
				original := *filter
				*filter = "changed"
				if r.Query.Get("filter") != original {
					t.Fatal("request retained mutable input")
				}
			}
			for _, query := range []string{"dc", "ns", "partition", "index", "wait", "consistent", "stale", "near", "passing", "hash", "token"} {
				bad := r.Clone()
				if bad.Query == nil {
					bad.Query = url.Values{}
				}
				bad.Query.Set(query, "")
				if _, _, _, err := validateRequest(bad); !errors.Is(err, ErrInvalid) {
					t.Fatal("local agent accepted unsupported scope", query, err)
				}
			}
		}
		for _, filter := range []string{strings.Repeat("x", 4097), "invalid\nfilter", string([]byte{0xff})} {
			if _, err := build(&filter); !errors.Is(err, ErrInvalid) {
				t.Fatal("invalid original filter accepted", err)
			}
		}
	}
	for _, r := range []Request{
		{Method: "PUT", Path: "/v1/agent/services"},
		{Method: "DELETE", Path: "/v1/agent/checks"},
		{Method: "GET", Path: "/v1/agent/checks", Body: []byte("{}")},
		{Method: "GET", Path: "/v1/agent/services", Query: url.Values{"filter": {"one", "two"}}},
		{Method: "GET", Path: "/v1/agent/service/foreign"},
		{Method: "PUT", Path: "/v1/agent/service/register", Body: []byte(`{"Name":"foreign"}`)},
	} {
		if _, _, _, err := validateRequest(r); err == nil {
			t.Fatal("local read expanded authority", r.Method, r.Path)
		}
	}
}

func TestAgentLocalReadsIgnoreDatacenterPrefixAndConsistencyAndRetainOriginalBytes(t *testing.T) {
	body := []byte(" {\"service:原值\": {\"ID\":\"service:原值\",\"future\":18446744073709551615,\"empty\":null}}\n")
	var calls atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Query().Encode() != "filter=" && r.URL.Query().Encode() != "" || r.Header.Get("X-Consul-Token") != "temporary-original" {
			t.Error("agent read inherited catalog scope or changed authentication")
		}
		b, _ := io.ReadAll(r.Body)
		if len(b) != 0 || r.Method != "GET" {
			t.Error("local agent read became a write")
		}
		w.Header().Set("X-Original", "原值")
		w.Write(body)
	}))
	defer s.Close()
	for _, consistency := range []string{"default", "consistent", "stale"} {
		c, err := New(Config{Endpoint: s.URL, Datacenter: "foreign-dc", Prefix: "foreign-prefix/", Consistency: consistency, Auth: "token"}, "temporary-original")
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range []Request{AgentSelfRequest(), mustAgentRequest(t)(AgentServicesRequest(nil)), mustAgentRequest(t)(AgentChecksRequest(str("")))} {
			plan, err := c.Prepare(r)
			if err != nil || plan.Writes() {
				t.Fatal("agent preparation changed read authority", err)
			}
			before := calls.Load()
			url, err := c.requestURL(plan)
			if err != nil || strings.Contains(url, "dc=") || strings.Contains(url, "stale") || strings.Contains(url, "consistent") || calls.Load() != before {
				t.Fatal("agent preparation dispatched or inherited foreign scope", err)
			}
			response, err := c.Execute(context.Background(), plan)
			if err != nil || !response.Ended || !response.Acknowledged || !response.Captured || !bytes.Equal(response.Body, body) || response.Header.Get("X-Original") != "原值" {
				t.Fatal("original agent response changed", err)
			}
			response.Body[0] = 'x'
			if !bytes.Equal(c.LastResponse().Body, body) {
				t.Fatal("inspection mutated original agent response")
			}
		}
		c.Close()
	}
	if calls.Load() != 9 {
		t.Fatal("agent read retried or preparation dispatched", calls.Load())
	}
}

func mustAgentRequest(t *testing.T) func(Request, error) Request {
	t.Helper()
	return func(r Request, err error) Request {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
}

func TestAgentLocalReadsPreserveIndependentUnknownWrite(t *testing.T) {
	var writes atomic.Int32
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "PUT" {
			writes.Add(1)
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte("original unknown write"))
			return
		}
		w.Write([]byte(`{"original-agent":{}}`))
	})
	if _, err := c.Do(context.Background(), putRequest("original", []byte("original value"))); err == nil {
		t.Fatal("unknown original write promoted")
	}
	before, ok := c.UnknownResponse()
	if !ok {
		t.Fatal("original unknown receipt missing")
	}
	for _, r := range []Request{mustAgentRequest(t)(AgentServicesRequest(nil)), mustAgentRequest(t)(AgentChecksRequest(nil))} {
		if _, err := c.Do(context.Background(), r); err != nil {
			t.Fatal("independent local read unavailable", err)
		}
	}
	after, ok := c.UnknownResponse()
	if !ok || !reflect.DeepEqual(before, after) || writes.Load() != 1 {
		t.Fatal("local read changed or replayed original unknown write")
	}
	if _, err := c.Prepare(putRequest("later", nil)); !errors.Is(err, ErrWriteUnknown) {
		t.Fatal("agent reads removed original write barrier", err)
	}
}
