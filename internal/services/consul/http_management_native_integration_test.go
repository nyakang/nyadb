package consul

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"github.com/nyakang/nyadb/internal/db/contract"
	"net/url"
	"os"
	"testing"
)

func TestManagementNativeOriginalQueriesEventsMeshAndOperator(t *testing.T) {
	cfg, token := nativeConfig(t)
	cfg.AutopilotWrite = true
	cfg.KeyringWrite = true
	s, err := OpenNativeSession(context.Background(), 907, cfg, token)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	l, err := s.ReserveKV(91)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	f := l.(contract.KVHTTPManaging)
	execute := func(in contract.KVHTTPManagementRequest) contract.KVHTTPManagementResult {
		t.Helper()
		p, err := f.PrepareHTTPManagement(in)
		if err != nil {
			t.Fatal(in.Operation, "prepare", err)
		}
		r, err := f.ExecuteHTTPManagement(context.Background(), p)
		if err != nil || !r.Receipt.Completed || r.Receipt.HTTP == nil || !r.Receipt.HTTP.Succeeded {
			status := 0
			if r.Receipt.HTTP != nil {
				status = r.Receipt.HTTP.Status
			}
			t.Fatalf("%s native status %d error %v", in.Operation, status, err)
		}
		return r
	}
	for _, op := range []string{"agent-members", "agent-metrics", "leader", "peers", "autopilot-config", "autopilot-health", "autopilot-state", "raft-config", "usage", "query-list", "event-list", "coordinate-nodes", "intentions-list", "peering-list", "exported-services"} {
		execute(contract.KVHTTPManagementRequest{Operation: op})
	}
	autopilot := execute(contract.KVHTTPManagementRequest{Operation: "autopilot-config"})
	var config struct{ ModifyIndex json.Number }
	if json.Unmarshal(autopilot.Receipt.HTTP.Body, &config) != nil || config.ModifyIndex == "" {
		t.Fatal("Autopilot index missing")
	}
	execute(contract.KVHTTPManagementRequest{Operation: "autopilot-update", Options: url.Values{"cas": {config.ModifyIndex.String()}}, Body: autopilot.Receipt.HTTP.Body})
	name := "nyadb-complete-query-" + rand.Text()
	create := execute(contract.KVHTTPManagementRequest{Operation: "query-create", Body: []byte(`{"Name":"` + name + `","Service":{"Service":"consul"}}`)})
	var query struct{ ID string }
	if json.Unmarshal(create.Receipt.HTTP.Body, &query) != nil || query.ID == "" {
		t.Fatal("query ID missing")
	}
	for _, op := range []string{"query-get", "query-execute", "query-explain"} {
		execute(contract.KVHTTPManagementRequest{Operation: op, Identity: query.ID})
	}
	execute(contract.KVHTTPManagementRequest{Operation: "query-update", Identity: query.ID, Body: []byte(`{"ID":"` + query.ID + `","Name":"` + name + `","Service":{"Service":"consul"}}`)})
	execute(contract.KVHTTPManagementRequest{Operation: "query-delete", Identity: query.ID})
	execute(contract.KVHTTPManagementRequest{Operation: "event-fire", Identity: name, Body: []byte{0, 255, 1}})
	mesh := contract.KVHTTPManagementRequest{Operation: "config-apply", Options: url.Values{"cas": {"0"}}, Body: []byte(`{"Kind":"service-defaults","Name":"` + name + `","Protocol":"http"}`)}
	execute(mesh)
	details := execute(contract.KVHTTPManagementRequest{Operation: "config-get", Kind: "service-defaults", Identity: name})
	var entry struct{ ModifyIndex json.Number }
	if json.Unmarshal(details.Receipt.HTTP.Body, &entry) != nil || entry.ModifyIndex == "" {
		t.Fatal("mesh index missing")
	}
	execute(contract.KVHTTPManagementRequest{Operation: "config-delete", Kind: "service-defaults", Identity: name, Options: url.Values{"cas": {entry.ModifyIndex.String()}}})
	execute(contract.KVHTTPManagementRequest{Operation: "intention-exact-put", Options: url.Values{"source": {name}, "destination": {name + "-dest"}}, Body: []byte(`{"Action":"allow"}`)})
	execute(contract.KVHTTPManagementRequest{Operation: "intention-exact-get", Options: url.Values{"source": {name}, "destination": {name + "-dest"}}})
	execute(contract.KVHTTPManagementRequest{Operation: "intention-check", Options: url.Values{"source": {name}, "destination": {name + "-dest"}}})
	execute(contract.KVHTTPManagementRequest{Operation: "intention-exact-delete", Options: url.Values{"source": {name}, "destination": {name + "-dest"}}})
	execute(contract.KVHTTPManagementRequest{Operation: "discovery-chain", Identity: name})
	if os.Getenv("NYADB_CONSUL_KEYRING_IT") == "1" {
		execute(contract.KVHTTPManagementRequest{Operation: "keyring-list"})
		key := base64.StdEncoding.EncodeToString([]byte("nyadb-gossip-key"))
		body := []byte(`{"Key":"` + key + `"}`)
		execute(contract.KVHTTPManagementRequest{Operation: "keyring-install", Body: body})
		if original := os.Getenv("NYADB_CONSUL_KEYRING_ORIGINAL"); original != "" {
			execute(contract.KVHTTPManagementRequest{Operation: "keyring-use", Body: body})
			execute(contract.KVHTTPManagementRequest{Operation: "keyring-use", Body: []byte(`{"Key":"` + original + `"}`)})
		}
		execute(contract.KVHTTPManagementRequest{Operation: "keyring-remove", Body: body})
	}
}
