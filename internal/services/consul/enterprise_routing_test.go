package consul

import (
	"net/url"
	"testing"
)

func TestEnterpriseEndpointScopesPreserveOriginalAuthority(t *testing.T) {
	c, err := New(Config{Endpoint: "http://original:8500", Datacenter: "dc1", Namespace: "ns1", Partition: "p1", Consistency: "consistent"}, "")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	for _, tc := range []struct {
		path, kind                     string
		dc, ns, partition, consistency bool
	}{
		{"/v1/kv/key", "kv-read", true, true, true, true},
		{"/v1/txn", "txn", true, true, true, true},
		{"/v1/catalog/nodes", "catalog-nodes", true, false, true, true},
		{"/v1/catalog/services", "catalog-services", true, true, true, true},
		{"/v1/catalog/datacenters", "catalog-datacenters", false, false, false, false},
		{"/v1/session/list", "session-list", true, true, true, true},
		{"/v1/health/state/any", "health-state", true, true, true, true},
		{"/v1/agent/services", "agent-services", false, true, true, false},
		{"/v1/agent/checks", "agent-checks", false, true, true, false},
		{"/v1/agent/self", "agent-self", false, false, false, false},
		{"/v1/status/leader", "leader", true, false, false, false},
		{"/v1/acl/templated-policies", "acl-templates", false, false, false, false},
		{"/v1/config/service-defaults", "management:config-list", true, true, true, true},
		{"/v1/peerings", "management:peering-list", true, false, true, true},
		{"/v1/namespaces", "management:namespace-list", true, false, true, true},
		{"/v1/snapshot", "snapshot-restore", true, false, false, false},
	} {
		t.Run(tc.path, func(t *testing.T) {
			target, err := c.requestURL(Plan{kind: tc.kind, request: Request{Method: "GET", Path: tc.path}})
			if err != nil {
				t.Fatal(err)
			}
			u, _ := url.Parse(target)
			q := u.Query()
			for key, want := range map[string]bool{"dc": tc.dc, "ns": tc.ns, "partition": tc.partition, "consistent": tc.consistency} {
				if q.Has(key) != want {
					t.Fatalf("%s: scope %s presence %t, want %t", target, key, q.Has(key), want)
				}
			}
		})
	}
}

func TestCapabilityProbeAliasesDoNotReplaceNativeCatalogHealthSessionOrACLValidation(t *testing.T) {
	for _, r := range []Request{
		{Method: "GET", Path: "/v1/health/state/any", Query: url.Values{"filter": {"ServiceName == `original`"}}},
		{Method: "GET", Path: "/v1/session/list", Query: url.Values{"index": {"9"}, "wait": {"1s"}}},
		{Method: "GET", Path: "/v1/acl/auth-methods"},
		{Method: "GET", Path: "/v1/agent/self"},
	} {
		kind, _, _, err := validateRequest(r)
		if err != nil || kind == "management:probe-health" || kind == "management:agent-self" {
			t.Fatal(r.Path, kind, err)
		}
	}
}
