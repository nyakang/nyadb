package consul

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func TestManagementNativePeeringOriginalTokenEstablishAndDelete(t *testing.T) {
	cfg, token := nativeConfig(t)
	endpoint, address := os.Getenv("NYADB_CONSUL_PEERING_ENDPOINT"), os.Getenv("NYADB_CONSUL_PEERING_SERVER")
	if endpoint == "" || address == "" {
		t.Skip("second owned Consul cluster and reachable gRPC TLS address required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	open := func(id int64, cfg Config, token string) contract.KVHTTPManaging {
		t.Helper()
		s, err := OpenNativeSession(ctx, id, cfg, token)
		if err != nil {
			t.Fatal("peering original session", err)
		}
		t.Cleanup(func() { s.Close() })
		l, err := s.ReserveKV(uint64(id))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { l.Close() })
		return l.(contract.KVHTTPManaging)
	}
	local := open(908, cfg, token)
	remoteConfig := Config{Endpoint: endpoint, Auth: os.Getenv("NYADB_CONSUL_PEERING_AUTH")}
	remote := open(909, remoteConfig, os.Getenv("NYADB_CONSUL_PEERING_TOKEN"))
	execute := func(f contract.KVHTTPManaging, in contract.KVHTTPManagementRequest) contract.KVHTTPManagementResult {
		t.Helper()
		p, err := f.PrepareHTTPManagement(in)
		if err != nil {
			t.Fatal(in.Operation, "preparation failed", err)
		}
		r, err := f.ExecuteHTTPManagement(ctx, p)
		if err != nil || !r.Receipt.Completed || r.Receipt.HTTP == nil || !r.Receipt.HTTP.Succeeded {
			status := 0
			if r.Receipt.HTTP != nil {
				status = r.Receipt.HTTP.Status
			}
			t.Fatalf("%s HTTP %d: complete %t, unknown %t, error %v", in.Operation, status, r.Receipt.Completed, r.Receipt.WriteOutcomeUnknown, err)
		}
		return r
	}
	name := "nyadb-peering-" + strings.ToLower(rand.Text())
	body, _ := json.Marshal(map[string]any{"PeerName": name, "ServerExternalAddresses": []string{address}})
	issued := execute(remote, contract.KVHTTPManagementRequest{Operation: "peering-token", Body: body})
	var issuedToken struct{ PeeringToken string }
	if json.Unmarshal(issued.Receipt.HTTP.Body, &issuedToken) != nil || issuedToken.PeeringToken == "" {
		t.Fatal("missing original private peering token")
	}
	body, _ = json.Marshal(map[string]string{"PeerName": name, "PeeringToken": issuedToken.PeeringToken})
	execute(local, contract.KVHTTPManagementRequest{Operation: "peering-establish", Body: body})
	for _, f := range []contract.KVHTTPManaging{local, remote} {
		r := execute(f, contract.KVHTTPManagementRequest{Operation: "peering-get", Identity: name})
		var peer struct{ Name string }
		if json.Unmarshal(r.Receipt.HTTP.Body, &peer) != nil || peer.Name != name {
			t.Fatal("original named peering missing")
		}
	}
	execute(local, contract.KVHTTPManagementRequest{Operation: "peering-delete", Identity: name})
	execute(remote, contract.KVHTTPManagementRequest{Operation: "peering-delete", Identity: name})
}
