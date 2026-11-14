package consul

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"reflect"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func TestConsulNativeCatalogProjectionOriginalEndpointsMapsAndConfiguredScope(t *testing.T) {
	writer, cfg, token := nativeClient(t)
	service := "  owned-" + t.Name() + " 雪%?#++  "
	id := "owned-" + t.Name()
	nativeHealthFixturePUT(t, writer, "/v1/agent/service/register", map[string]any{"ID": id, "Name": service, "Address": "127.0.0.1", "Port": 8087, "Tags": []string{"original", "中文"}, "Meta": map[string]string{"original": "18446744073709551615"}})
	t.Cleanup(func() { nativeHealthFixturePUT(t, writer, "/v1/agent/service/deregister/"+url.PathEscape(id), nil) })
	var self struct{ Config struct{ NodeName string } }
	if json.Unmarshal(nativeDo(t, writer, AgentSelfRequest()).Body, &self) != nil || self.Config.NodeName == "" {
		t.Fatal("original catalog node unavailable")
	}
	node := self.Config.NodeName
	cfg.Datacenter, cfg.Prefix, cfg.Consistency = "dc1", "foreign-prefix/", "consistent"
	reader, err := New(cfg, token)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reader.Close() })
	build := nativeCatalogRequest(t)
	serviceRequest := build(CatalogServiceNodesRequest(service))
	deadline := time.Now().Add(10 * time.Second)
	for {
		var rows []json.RawMessage
		if json.Unmarshal(nativeDo(t, reader, serviceRequest).Body, &rows) == nil && len(rows) == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("owned original service never reached catalog")
		}
		time.Sleep(50 * time.Millisecond)
	}
	index, wait, empty, near := uint64(0), "1ms", "", "_agent"
	for _, item := range []struct {
		request Request
		filter  string
	}{
		{CatalogNodesRequest(), "Node == " + strconv.Quote(node)},
		{CatalogServicesRequest(), "ServiceName == " + strconv.Quote(service)},
		{serviceRequest, "ServiceName == " + strconv.Quote(service)},
		{build(CatalogNodeServicesRequest(node)), "Service == " + strconv.Quote(service)},
	} {
		for _, filter := range []*string{nil, &empty, &item.filter} {
			options := CatalogReadOptions{Filter: filter, Index: &index, Wait: &wait}
			kind := catalogReadKind(item.request.Path)
			if kind == "nodes" || kind == "service" {
				options.Near = &near
			}
			request := build(WithCatalogReadOptions(item.request, options))
			response := nativeDo(t, reader, request)
			actual, err := url.Parse(response.URL)
			if err != nil || actual.Query().Get("dc") != "dc1" || !actual.Query().Has("consistent") || actual.Query().Has("filter") != (filter != nil) || actual.Query().Get("wait") != wait || actual.Query().Get("index") != "0" || actual.Query().Has("near") != (options.Near != nil) || !reflect.DeepEqual(response.Request.Query, request.Query) {
				t.Fatal("catalog request lost original scope, name or option presence", err)
			}
			if response.Metadata.Index == nil || *response.Metadata.Index == 0 || !json.Valid(response.Body) || !response.Succeeded || response.WriteOutcomeUnknown {
				t.Fatal("catalog read invented index, authority or complete receipt")
			}
			if filter == &item.filter {
				switch kind {
				case "nodes", "service":
					var rows []map[string]json.RawMessage
					if json.Unmarshal(response.Body, &rows) != nil || len(rows) != 1 {
						t.Fatal("catalog filter did not retain one original node/instance", kind)
					}
					if kind == "service" {
						var name, originalID string
						if json.Unmarshal(rows[0]["ServiceName"], &name) != nil || json.Unmarshal(rows[0]["ServiceID"], &originalID) != nil || name != service || originalID != id {
							t.Fatal("catalog normalized original service identity")
						}
					}
				case "services":
					var rows map[string][]string
					if json.Unmarshal(response.Body, &rows) != nil || len(rows) != 1 {
						t.Fatal("catalog service map lost original key or tags")
					}
					// Native service listings aggregate tags through a set. Verify
					// membership without assigning a stable cross-read order; the
					// complete original response bytes remain unchanged below.
					tags := slices.Clone(rows[service])
					slices.Sort(tags)
					if !slices.Equal(tags, []string{"original", "中文"}) {
						t.Fatal("catalog service map lost original tags")
					}
				case "node":
					var row struct{ Services map[string]json.RawMessage }
					if json.Unmarshal(response.Body, &row) != nil || len(row.Services) != 1 || row.Services[id] == nil {
						t.Fatal("catalog node filter lost original keyed service map")
					}
				}
			}
			projected := nativeAgentProjection(t, response)
			list := projected.HTTP.Catalog
			if list == nil || list.Kind != kind || !list.ProjectionComplete || !list.Complete || list.Null || projected.HTTP.Agent != nil || projected.HTTP.Health != nil || !bytes.Equal(projected.HTTP.Body, response.Body) {
				t.Fatal("original Catalog projection unavailable or mixed with other evidence", projected.ProjectionError)
			}
			if filter == &item.filter {
				switch kind {
				case "nodes":
					if len(list.Nodes) != 1 || list.Nodes[0].Node != node || list.Nodes[0].ModifyIndex == 0 || len(list.Nodes[0].Raw) == 0 {
						t.Fatal("original Catalog node projection lost identity/index/raw")
					}
				case "services":
					if len(list.Services) != 1 || list.Services[0].Name != service {
						t.Fatal("original Catalog service map key lost")
					}
					var nativeTags map[string][]string
					if json.Unmarshal(response.Body, &nativeTags) != nil || !reflect.DeepEqual(list.Services[0].Tags, nativeTags[service]) {
						t.Fatal("Catalog projection reordered native tags")
					}
				case "service":
					if len(list.Instances) != 1 || list.Instances[0].Node != node || list.Instances[0].ServiceName != service || list.Instances[0].ServiceID != id || list.Instances[0].ServiceMeta["original"] != "18446744073709551615" || list.Instances[0].ServicePort != 8087 || list.Instances[0].ModifyIndex == 0 {
						t.Fatal("flat Catalog service-instance projection changed native fields")
					}
				case "node":
					if list.NodeServices == nil || list.NodeServices.Node.Node != node || len(list.NodeServices.Services) != 1 || list.NodeServices.Services[0].Key != id || list.NodeServices.Services[0].Service.Service != service {
						t.Fatal("Catalog node projection changed original keyed service")
					}
				}
			}
			clone := projected.Clone()
			clone.HTTP.Body[0] ^= 1
			if !bytes.Equal(projected.HTTP.Body, response.Body) {
				t.Fatal("Catalog receipt clone shares original body")
			}
			without := projected.Clone()
			without.HTTP.Catalog = nil
			if resultCost(&projected)-resultCost(&without) != catalogProjectionCost(*list) {
				t.Fatal("original Catalog lease omitted projection evidence accounting")
			}
			before := bytes.Clone(response.Body)
			response.Body[0] ^= 1
			if !bytes.Equal(reader.LastResponse().Body, before) {
				t.Fatal("catalog inspection mutated original cached receipt")
			}
		}
	}
	missing := nativeDo(t, reader, build(CatalogNodeServicesRequest("owned-no-such-catalog-node")))
	if !bytes.Equal(bytes.TrimSpace(missing.Body), []byte("null")) || !missing.Succeeded {
		t.Fatal("absent catalog node native null was changed")
	}
	missingProjection := nativeAgentProjection(t, missing)
	if missingProjection.HTTP.Catalog == nil || !missingProjection.HTTP.Catalog.Null || missingProjection.HTTP.Catalog.NodeServices != nil || !missingProjection.HTTP.Catalog.Complete {
		t.Fatal("original native null became an empty or missing projection", missingProjection.ProjectionError)
	}
	dcs := nativeDo(t, reader, CatalogDatacentersRequest())
	var datacenters []string
	if json.Unmarshal(dcs.Body, &datacenters) != nil || !reflect.DeepEqual(datacenters, []string{"dc1"}) {
		t.Fatal("original global datacenter list changed")
	}
	dcProjection := nativeAgentProjection(t, dcs)
	if dcProjection.HTTP.Catalog == nil || !reflect.DeepEqual(dcProjection.HTTP.Catalog.Datacenters, datacenters) {
		t.Fatal("Catalog datacenter projection changed original order")
	}
	if cfg.Auth == "token" {
		policy := nativeAgentServiceEvidence(t, writer, "/v1/acl/policy", nativeAgentServiceEvidenceJSON(t, map[string]any{"Name": id + "-deny", "Rules": `node_prefix "" {policy="deny"} service_prefix "" {policy="deny"}`}))
		var policyID struct{ ID string }
		if policy.Status != 200 || json.Unmarshal(policy.Body, &policyID) != nil || policyID.ID == "" {
			t.Fatal("owned catalog denial policy unavailable")
		}
		created := nativeAgentServiceEvidence(t, writer, "/v1/acl/token", nativeAgentServiceEvidenceJSON(t, map[string]any{"Policies": []map[string]string{{"ID": policyID.ID}}}))
		var credential struct{ SecretID string }
		if created.Status != 200 || json.Unmarshal(created.Body, &credential) != nil || credential.SecretID == "" {
			t.Fatal("owned catalog denial token unavailable")
		}
		limited, err := New(cfg, credential.SecretID)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { limited.Close() })
		for _, request := range []Request{CatalogNodesRequest(), CatalogServicesRequest(), serviceRequest, build(CatalogNodeServicesRequest(node))} {
			response, err := limited.Do(context.Background(), request)
			if err != nil || !response.Captured || !response.EOF || !response.Acknowledged || !response.Succeeded || response.Metadata.ACLFiltered == nil || !*response.Metadata.ACLFiltered || response.WriteOutcomeUnknown {
				t.Fatal("catalog ACL-filtered receipt lost completeness boundary", request.Path, response.Status, err)
			}
			projection := nativeAgentProjection(t, response)
			if projection.HTTP.Catalog == nil || !projection.HTTP.Catalog.ProjectionComplete || projection.HTTP.Catalog.Complete || !projection.Completed || projection.WriteOutcomeUnknown {
				t.Fatal("ACL-filtered native Catalog response acquired absence or write authority", projection.ProjectionError)
			}
			if bytes.Contains(response.Body, []byte(id)) || bytes.Contains(response.Body, []byte(node)) {
				t.Fatal("catalog ACL-filtered read exposed denied original entries")
			}
		}
	}
}

func TestConsulNativeCatalogProjectionFaultsCancellationAndOriginalNoRetry(t *testing.T) {
	writer, cfg, token := nativeClient(t)
	nativeHealthFixture(t, writer)
	for _, mode := range []string{"lost", "partial", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			proxy, calls, observed := nativeAgentReadRelay(t, cfg, token, mode)
			reader, err := New(proxy, token)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { reader.Close() })
			plan, err := reader.Prepare(CatalogServicesRequest())
			if err != nil || calls.Load() != 0 {
				t.Fatal("catalog preparation dispatched", err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			type completion struct {
				response Response
				err      error
			}
			done := make(chan completion, 1)
			go func() { response, err := reader.Execute(ctx, plan); done <- completion{response, err} }()
			select {
			case <-observed:
			case <-time.After(30 * time.Second):
				t.Fatal("original catalog upstream did not complete")
			}
			if mode == "cancel" {
				cancel()
			}
			select {
			case result := <-done:
				projection := nativeAgentProjection(t, result.response)
				if projection.HTTP.Catalog != nil || !bytes.Equal(projection.HTTP.Body, result.response.Body) || projection.Completed || projection.WriteOutcomeUnknown {
					t.Fatal("processed Catalog read fault acquired a projection or write outcome")
				}
				if result.err == nil || !result.response.Ended || result.response.Acknowledged || result.response.WriteOutcomeUnknown || calls.Load() != 1 || mode == "partial" && len(result.response.Body) == 0 || mode == "cancel" && !result.response.Canceled {
					t.Fatal("catalog fault was promoted, lost or retried", result.err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("catalog executor did not acknowledge cancellation")
			}
			if _, err := reader.Execute(context.Background(), plan); !errors.Is(err, ErrRetired) || calls.Load() != 1 {
				t.Fatal("catalog revoked executor gained replay authority", err)
			}
		})
	}
}

func TestConsulNativeCatalogProjectionRetainsOriginalUnknownWriteAndPureRecovery(t *testing.T) {
	writer, cfg, token := nativeClient(t)
	proxy, calls, _ := nativeFault(t, cfg, token, "http500")
	session, err := OpenNativeSession(context.Background(), 93, proxy, token)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { session.Close() })
	lease, err := session.ReserveKV(73)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { lease.Close() })
	key, value := keyFor(t, "original-unknown"), []byte{0, 255, 128}
	request, err := DocumentRequest(putRequest(key, value))
	if err != nil {
		t.Fatal(err)
	}
	plan, err := lease.Prepare(request)
	if err != nil {
		t.Fatal(err)
	}
	result, err := lease.Execute(context.Background(), plan)
	if err == nil || !result.WriteOutcomeUnknown || !bytes.Equal(nativeRead(t, writer, key).Value, value) {
		t.Fatal("catalog fixture write was not actually processed", err)
	}
	unknown := lease.(*documentLease).recoveryOwned().Unknown
	if unknown == nil {
		t.Fatal("original unknown write missing")
	}
	for _, native := range []Request{CatalogDatacentersRequest(), CatalogNodesRequest(), CatalogServicesRequest()} {
		read, err := DocumentRequest(native)
		if err != nil {
			t.Fatal(err)
		}
		readPlan, err := lease.Prepare(read)
		if err != nil {
			t.Fatal(err)
		}
		result, err := lease.Execute(context.Background(), readPlan)
		if err == nil && result.HTTP != nil && (result.HTTP.Catalog == nil || !result.HTTP.Catalog.ProjectionComplete) {
			t.Fatal("original document lease omitted Catalog projection", result.ProjectionError)
		}
		if err != nil || !result.Completed || result.HTTP == nil || !result.HTTP.Acknowledged || result.WriteOutcomeUnknown || !reflect.DeepEqual(unknown, lease.(*documentLease).recoveryOwned().Unknown) {
			t.Fatal("catalog read changed original unknown write", err)
		}
	}
	before := calls.Load()
	recovered, err := session.RecoverKV(lease)
	if err != nil || recovered.Lease == nil || calls.Load() != before || !reflect.DeepEqual(unknown, recovered.Unknown) {
		t.Fatal("catalog recovery dispatched or changed original evidence", err)
	}
	t.Cleanup(func() { recovered.Lease.Close() })
	if _, err := recovered.Lease.Prepare(request); !errors.Is(err, contract.ErrKVWriteUnknown) || calls.Load() != before {
		t.Fatal("catalog reads or recovery cleared write barrier", err)
	}
}
