package consul

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

func TestAgentMaintenanceLocalScopePreparationSnapshotsAndReceiptNeverAlias(t *testing.T) {
	for _, kind := range []string{"node", "service"} {
		var calls atomic.Int32
		reason := "original control\n\r\x00中文"
		c, _ := testClient(t, func(w http.ResponseWriter, incoming *http.Request) {
			calls.Add(1)
			query := incoming.URL.Query()
			if incoming.Method != http.MethodPut || query.Get("enable") != "true" || query.Get("reason") != reason || query.Has("dc") || query.Has("ns") || query.Has("partition") || query.Has("consistent") || query.Has("stale") {
				t.Error("maintenance inherited catalog scope or lost original inputs")
			}
			w.Header().Set("X-Original-Receipt", "retained")
			w.WriteHeader(http.StatusOK)
		})
		c.config.Datacenter, c.config.Prefix, c.config.Consistency = "foreign", "foreign/", "consistent"
		r, err := AgentNodeMaintenanceRequest(true, &reason)
		if kind == "service" {
			r, err = AgentServiceMaintenanceRequest("original 雪%?#++", true, &reason)
		}
		if err != nil {
			t.Fatal(err)
		}
		original := r.Clone()
		plan, err := c.Prepare(r)
		if err != nil || !plan.Writes() || calls.Load() != 0 {
			t.Fatal("maintenance preparation dispatched or lost write authority", err)
		}
		r.Query.Set("enable", "false")
		r.Query.Set("reason", "later request")
		inspection := plan.Snapshot()
		inspection.Query.Set("reason", "later inspection")
		result, err := c.Execute(context.Background(), plan)
		if err != nil || !result.Captured || !result.EOF || !result.Acknowledged || !result.Succeeded || result.WriteOutcomeUnknown || !reflect.DeepEqual(result.Request.Query, original.Query) || result.Request.Path != original.Path || len(result.Body) != 0 || result.Txn != nil {
			t.Fatal("original maintenance local ACK changed or synthesized state", err)
		}
		if _, err := c.Execute(context.Background(), plan); !errors.Is(err, ErrPlanUsed) || calls.Load() != 1 {
			t.Fatal("maintenance replayed original authority", err)
		}
		result.Request.Query.Set("reason", "later receipt inspection")
		result.Header.Set("X-Original-Receipt", "later")
		if !reflect.DeepEqual(c.LastResponse().Request.Query, original.Query) || c.LastResponse().Header.Get("X-Original-Receipt") != "retained" {
			t.Fatal("maintenance receipt retained mutable inspection")
		}
	}
}

func TestAgentMaintenanceRequestsRetainOriginalIdentityReasonPresenceAndCodec(t *testing.T) {
	id := "original 雪%?#++"
	for _, kind := range []string{"node", "service"} {
		for _, enable := range []bool{true, false} {
			for _, authored := range []*string{nil, new(string), ttlTestNote("  original first\nsecond\r\nNUL\x00雪 %?#++  "), ttlTestNote(strings.Repeat("x", 8192))} {
				want := ""
				if authored != nil {
					want = *authored
				}
				var r Request
				var err error
				path := "/v1/agent/maintenance"
				if kind == "node" {
					r, err = AgentNodeMaintenanceRequest(enable, authored)
				} else {
					path = "/v1/agent/service/maintenance/" + url.PathEscape(id)
					r, err = AgentServiceMaintenanceRequest(id, enable, authored)
				}
				if err != nil || r.Method != http.MethodPut || r.Path != path || r.Body != nil || r.Query.Has("reason") != (authored != nil) || r.Query.Get("reason") != want || r.Query.Get("enable") != map[bool]string{true: "true", false: "false"}[enable] {
					t.Fatal("maintenance changed original request/presence", kind, enable, err)
				}
				if authored != nil {
					*authored = "later mutable reason"
				}
				doc, err := DocumentRequest(r)
				native, canonical, writes, decodeErr := decodeDocumentRequest(doc)
				if err != nil || decodeErr != nil || !writes || canonical != doc || !reflect.DeepEqual(native.Query, r.Query) || native.Path != path || native.Query.Get("reason") != want {
					t.Fatal("maintenance codec lost original identity/presence", err, decodeErr)
				}
			}
		}
	}
}

func TestAgentMaintenanceRawNativeBooleansRemainExactAndAmbiguitiesReject(t *testing.T) {
	for _, path := range []string{"/v1/agent/maintenance", "/v1/agent/service/maintenance/original"} {
		for _, value := range []string{"1", "t", "T", "TRUE", "true", "True", "0", "f", "F", "FALSE", "false", "False"} {
			r := Request{Method: http.MethodPut, Path: path, Query: url.Values{"enable": {value}}}
			doc, err := DocumentRequest(r)
			native, _, writes, decodeErr := decodeDocumentRequest(doc)
			if err != nil || decodeErr != nil || !writes || native.Query.Get("enable") != value {
				t.Fatal("native lexical boolean silently normalized", value, err, decodeErr)
			}
		}
		for _, query := range []url.Values{nil, {"enable": {}}, {"enable": {""}}, {"enable": {"truth"}}, {"enable": {"2"}}, {"enable": {" true"}}, {"enable": {"true", "false"}}, {"enable": {"true"}, "reason": {"first", "later"}}, {"enable": {"true"}, "reason": {string([]byte{0xff})}}, {"enable": {"true"}, "dc": {"dc1"}}, {"enable": {"true"}, "namespace": {"hidden"}}, {"enable": {"true"}, "partition": {"hidden"}}, {"enable": {"true"}, "token": {"hidden"}}, {"enable": {"true"}, "consistent": {""}}, {"enable": {"true"}, "note": {"ignored"}}} {
			if _, err := DocumentRequest(Request{Method: http.MethodPut, Path: path, Query: query}); !errors.Is(err, ErrInvalid) {
				t.Fatal("maintenance ambiguity/foreign scope silently accepted", err)
			}
		}
		for _, method := range []string{http.MethodGet, http.MethodDelete, http.MethodPost} {
			if _, err := DocumentRequest(Request{Method: method, Path: path, Query: url.Values{"enable": {"true"}}}); err == nil {
				t.Fatal("non-native maintenance method granted authority", method)
			}
		}
		if _, err := DocumentRequest(Request{Method: http.MethodPut, Path: path, Query: url.Values{"enable": {"false"}}, Body: []byte("ignored native body")}); err == nil {
			t.Fatal("maintenance silently discarded original body")
		}
	}
}

func TestAgentMaintenanceRejectsInvalidIdentityAndCompleteReasonBounds(t *testing.T) {
	for _, id := range []string{"", "invalid\nid", "NUL\x00id", string([]byte{0xff}), strings.Repeat("x", 4097)} {
		if _, err := AgentServiceMaintenanceRequest(id, true, nil); !errors.Is(err, ErrInvalid) {
			t.Fatal("maintenance invalid original ID accepted", err)
		}
		if _, err := DocumentRequest(Request{Method: http.MethodPut, Path: "/v1/agent/service/maintenance/" + url.PathEscape(id), Query: url.Values{"enable": {"true"}}}); err == nil {
			t.Fatal("raw maintenance bypassed original ID validation")
		}
	}
	for _, id := range []string{"a//b", "a/../b", "/leading", "trailing/"} {
		if _, err := AgentServiceMaintenanceRequest(id, false, nil); !errors.Is(err, ErrUnsupported) {
			t.Fatal("maintenance cleaned original service ID", err)
		}
	}
	for _, reason := range []string{strings.Repeat("x", requestLimit+1), string([]byte{0xff})} {
		if _, err := AgentNodeMaintenanceRequest(true, &reason); err == nil {
			t.Fatal("maintenance accepted invalid or incomplete reason")
		}
		if _, err := AgentServiceMaintenanceRequest("original", false, &reason); err == nil {
			t.Fatal("service maintenance reason bypassed complete bounds")
		}
	}
}

func TestAgentMaintenanceReceiptsSeparateEmptyACKFromStateAndUnknownErrors(t *testing.T) {
	for _, kind := range []string{"agent-node-maintenance", "agent-service-maintenance"} {
		for _, status := range []int{200, 201, 400, 403, 404, 408, 500} {
			for _, body := range [][]byte{nil, []byte(" "), []byte("null"), []byte("true"), []byte("false"), []byte("original error")} {
				r := Response{Status: status, Body: bytes.Clone(body)}
				err := projectResponse(&r, Plan{kind: kind})
				known := status == 403 || status == 404 && kind == "agent-service-maintenance"
				success := status == 200 && len(body) == 0
				if r.Acknowledged != (known || success) || r.Succeeded != success || (err == nil) != success || !bytes.Equal(r.Body, body) || r.Txn != nil {
					t.Fatal("maintenance ACK synthesized state or guessed unknown outcome", kind, status, err)
				}
			}
		}
	}
}
