package consul

import (
	"bytes"
	"context"
	"errors"
	"github.com/nyakang/nyadb/internal/db/contract"
	"net/http"
	"net/url"
	"reflect"
	"testing"
)

func TestManagementPreparationsCaptureOriginalWithoutHTTPAndIsolateGeneric(t *testing.T) {
	s, calls := documentSession(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"ID":"aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"}`))
	})
	l, _ := s.ReserveKV(1)
	defer l.Close()
	f := l.(contract.KVHTTPManaging)
	in := contract.KVHTTPManagementRequest{Operation: "query-create", Body: []byte(`{"Name":"private-query","Service":{"Service":"original"},"Token":"private-embedded-token"}`)}
	before := calls.Load()
	p, err := f.PrepareHTTPManagement(in)
	if err != nil || calls.Load() != before || !p.Writes() {
		t.Fatal("preparation sent HTTP", err)
	}
	saved := p.Request()
	in.Body[0] = '!'
	inspection := p.Request()
	inspection.Body[0] = '!'
	if !reflect.DeepEqual(saved, p.Request()) {
		t.Fatal("plan aliases mutable input")
	}
	if _, err := DocumentRequest(Request{Method: p.HTTP().Method, Path: p.HTTP().Path, Query: p.HTTP().Query, Body: p.HTTP().Body}); err == nil {
		t.Fatal("private route entered generic files")
	}
	other, _ := s.ReserveKV(2)
	defer other.Close()
	if _, err = other.(contract.KVHTTPManaging).ExecuteHTTPManagement(context.Background(), p); !errors.Is(err, contract.ErrKVPlanUsed) {
		t.Fatal("foreign plan accepted", err)
	}
	r, err := f.ExecuteHTTPManagement(context.Background(), p)
	if err != nil || !r.Receipt.Completed || !r.Receipt.HTTP.Succeeded || !reflect.DeepEqual(saved, r.Request) || l.LastResult().HTTP != nil {
		t.Fatal("original private receipt lost", err)
	}
	r.Request.Body[0] = '!'
	r.Receipt.HTTP.Body[0] = '!'
	if !reflect.DeepEqual(saved, f.LastHTTPManagement().Request) {
		t.Fatal("inspection mutated cache")
	}
	if _, err = f.ExecuteHTTPManagement(context.Background(), p); !errors.Is(err, contract.ErrKVPlanUsed) {
		t.Fatal("plan replayed", err)
	}
}

func TestManagementRejectsMalformedDefinitionsScopeAndNativeAcknowledgements(t *testing.T) {
	invalid := []contract.KVHTTPManagementRequest{
		{Operation: "config-apply", Body: []byte(`{"Kind":"service-defaults","Name":"s"}`)},
		{Operation: "config-delete", Kind: "service-defaults", Identity: "s"},
		{Operation: "query-update", Identity: "different", Body: []byte(`{"ID":"aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee","Service":{"Service":"s"}}`)},
		{Operation: "agent-members", Options: url.Values{"ns": {"foreign"}}},
		{Operation: "agent-metrics", Options: url.Values{"format": {"json"}}},
		{Operation: "raft-remove", Options: url.Values{"id": {"x"}, "address": {"x"}}},
		{Operation: "event-fire", Identity: "_reserved"},
		{Operation: "keyring-install", Body: []byte(`{"Key":"invalid"}`)},
		{Operation: "namespace-update", Identity: "original", Body: []byte(`{"Name":"foreign"}`)},
		{Operation: "peering-token", Body: []byte(`{"PeerName":"Uppercase"}`)},
		{Operation: "peering-establish", Body: []byte(`{"PeerName":"-leading","PeeringToken":"private"}`)},
		{Operation: "peering-delete", Identity: "trailing-"},
		{Operation: "config-apply", Options: url.Values{"cas": {"0"}}, Body: []byte(`{"Kind":"service-defaults","Name":"s","Namespace":"foreign"}`)},
	}
	for _, in := range invalid {
		if _, err := ManagementRequest(in); err == nil {
			t.Fatal("invalid management request accepted", in.Operation)
		}
	}
	for _, tc := range []struct{ op, body string }{{"autopilot-update", "{}"}, {"intention-update", "true"}, {"keyring-install", "{}"}, {"raft-transfer", "{}"}, {"peering-token", "{}"}, {"query-create", "null"}} {
		op, _ := managementSpec(tc.op)
		r := Response{Status: 200, Body: []byte(tc.body)}
		if err := projectManagementResponse(&r, Plan{request: Request{Path: "/v1/connect/intentions/aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"}}, op); err == nil || r.Acknowledged {
			t.Fatal("malformed write ACK promoted", tc.op)
		}
	}
}

func TestManagementUnknownWriteSurvivesReadsRecoveryAndGenericWriteBarrier(t *testing.T) {
	s, calls := documentSession(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			w.WriteHeader(500)
			w.Write([]byte("original unknown"))
			return
		}
		w.Write([]byte(`[]`))
	})
	l, _ := s.ReserveKV(1)
	f := l.(contract.KVHTTPManaging)
	p, err := f.PrepareHTTPManagement(contract.KVHTTPManagementRequest{Operation: "query-delete", Identity: "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"})
	if err != nil {
		t.Fatal(err)
	}
	unknown, err := f.ExecuteHTTPManagement(context.Background(), p)
	if err == nil || !unknown.Receipt.WriteOutcomeUnknown {
		t.Fatal("write uncertainty lost", err)
	}
	read, _ := f.PrepareHTTPManagement(contract.KVHTTPManagementRequest{Operation: "query-list"})
	if _, err = f.ExecuteHTTPManagement(context.Background(), read); err != nil {
		t.Fatal(err)
	}
	authored, _ := DocumentRequest(putRequest("later", nil))
	if _, err = l.Prepare(authored); !errors.Is(err, contract.ErrKVWriteUnknown) {
		t.Fatal("private unknown cleared generic barrier", err)
	}
	before := calls.Load()
	recovered, err := s.RecoverKV(l)
	if err != nil {
		t.Fatal(err)
	}
	defer recovered.Lease.Close()
	if calls.Load() != before || recovered.ManagementUnknown == nil || !reflect.DeepEqual(unknown, *recovered.ManagementUnknown) || recovered.ManagementResult == nil || recovered.ManagementResult.Request.Operation != "query-list" {
		t.Fatal("pure recovery lost original uncertainty", err)
	}
	if _, err = recovered.Lease.(contract.KVHTTPManaging).PrepareHTTPManagement(contract.KVHTTPManagementRequest{Operation: "query-delete", Identity: "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"}); !errors.Is(err, contract.ErrKVWriteUnknown) {
		t.Fatal("recovery cleared write barrier", err)
	}
	if bytes.Equal(recovered.ManagementResult.Receipt.HTTP.Body, unknown.Receipt.HTTP.Body) {
		t.Fatal("later read replaced unknown")
	}
}
