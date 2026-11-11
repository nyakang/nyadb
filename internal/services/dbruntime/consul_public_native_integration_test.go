package dbruntime_test

import (
	"context"
	"errors"
	"math"
	"net/url"
	"os"
	"strconv"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/consul"
	"github.com/nyakang/nyadb/internal/services/dbruntime"
)

func consulPublicProfile(t *testing.T) (connections.Profile, string) {
	t.Helper()
	cfg, secret := consulRuntimeConfig(t)
	u, err := url.Parse(cfg.Endpoint)
	if err != nil || u.Hostname() == "" {
		t.Fatal("missing owned endpoint")
	}
	p, _ := connections.Defaults(connections.Consul)
	p.ID, p.Name, p.Host = 902, "public Consul", u.Hostname()
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatal(err)
	}
	p.Port = uint16(port)
	p.ConsulOptions.Auth, p.ConsulOptions.Datacenter = cfg.Auth, "dc1"
	p.ConsulOptions.CAFile, p.ConsulOptions.ClientCertFile, p.ConsulOptions.ClientKeyFile = cfg.CAFile, cfg.ClientCertFile, cfg.ClientKeyFile
	if u.Scheme == "https" {
		p.TLSMode = "verify-full"
	}
	return p, secret
}
func TestConsulNativePublicBuiltinProfileMetadataCapabilitiesAndRestrictedToken(t *testing.T) {
	p, secret := consulPublicProfile(t)
	m := dbruntime.New()
	defer m.Close()
	dbruntime.RegisterBuiltins(m, nil)
	s, err := m.ConnectSession(context.Background(), p, secret)
	if err != nil {
		t.Fatal(err)
	}
	if s.Capabilities() != (contract.Capabilities{KeyValues: true, Metadata: true}) {
		t.Fatal("public provider advertised unsupported tools", s.Capabilities())
	}
	metadata, err := contract.Require[contract.MetadataProvider](s)
	if err != nil {
		t.Fatal(err)
	}
	ns, err := metadata.Namespaces(context.Background())
	if err != nil || len(ns) != 1 || ns[0].Label != "kv" || ns[0].Ref != (contract.NamespaceRef{ProfileID: p.ID, Catalog: "kv"}) {
		t.Fatal("DC was fabricated as a SQL catalog", err)
	}
	objects, err := metadata.Browse(context.Background(), ns[0].Ref)
	if err != nil || len(objects) != 0 {
		t.Fatal("native requests fabricated relational objects", err)
	}
	if _, err = metadata.Browse(context.Background(), contract.NamespaceRef{ProfileID: p.ID, Catalog: "foreign"}); !errors.Is(err, contract.ErrUnsupported) {
		t.Fatal("foreign namespace accepted", err)
	}
	query, err := contract.Require[contract.QueryProvider](s)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = query.ReserveQuery(1); !errors.Is(err, contract.ErrUnsupported) {
		t.Fatal("SQL execution published", err)
	}
	if data := m.Data(p.ID); data.Loader != nil || data.Saver != nil {
		t.Fatal("relational grid published")
	}
	if _, err = m.KVEvents(p.ID); !errors.Is(err, contract.ErrUnsupported) {
		t.Fatal("etcd Watch feature published", err)
	}
	if _, err = m.KVRenewal(p.ID); !errors.Is(err, contract.ErrUnsupported) {
		t.Fatal("numeric lease renewal published", err)
	}
	provider, err := m.KVRequests(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	presentation := provider.(contract.KVRequestPresentationProvider).KVRequestPresentation()
	if presentation.Product != "Consul" || len(presentation.Methods) != 1 || presentation.Default.Method != contract.KVConsulHTTPRequestMethod {
		t.Fatal("public HTTP presentation missing")
	}
	l, err := provider.ReserveKV(1)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	flags := uint64(math.MaxUint64)
	native, err := consul.KeyPutRequest("native/public-runtime/"+t.Name(), []byte{0, 255}, &flags, nil)
	if err != nil {
		t.Fatal(err)
	}
	request, err := consul.DocumentRequest(native)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := l.Prepare(request)
	if err != nil {
		t.Fatal(err)
	}
	result, err := l.Execute(context.Background(), plan)
	if err != nil || !result.Completed || result.HTTP == nil || !result.HTTP.Succeeded || !result.HTTP.Acknowledged {
		t.Fatal("public original write failed", err)
	}
	for _, token := range []string{os.Getenv("NYADB_CONSUL_READ_TOKEN"), os.Getenv("NYADB_CONSUL_WRITE_TOKEN")} {
		if token == "" {
			continue
		}
		limited := p
		limited.ID++
		limited.ConsulOptions.Prefix = "allowed/"
		limitedSession, err := m.ConnectSession(context.Background(), limited, token)
		if err != nil {
			t.Fatal("KV-only token could not create public profile", err)
		}
		limitedProvider, _ := m.KVRequests(limited.ID)
		scope := limitedProvider.KVScope()
		if scope.HTTP.AgentAvailable || scope.HTTP.AgentID != "" {
			t.Fatal("unavailable Agent identity was invented")
		}
		methods := limitedProvider.(contract.KVRequestPresentationProvider).KVRequestPresentation()
		nativeTemplate, _ := consul.KeyListRequest(limited.ConsulOptions.Prefix, true)
		expectedTemplate, _ := consul.DocumentRequest(nativeTemplate)
		if methods.Default != expectedTemplate || limitedSession.Capabilities() != s.Capabilities() {
			t.Fatal("original restricted prefix or product changed")
		}
		lease, err := limitedProvider.ReserveKV(1)
		if err != nil {
			t.Fatal(err)
		}
		native, _ := consul.KeyPutRequest("denied/public-runtime", []byte("denied"), nil, nil)
		request, _ := consul.DocumentRequest(native)
		plan, err := lease.Prepare(request)
		if err != nil {
			t.Fatal(err)
		}
		denied, err := lease.Execute(context.Background(), plan)
		lease.Close()
		if err == nil || denied.HTTP == nil || denied.HTTP.Status != 403 || !denied.HTTP.Acknowledged || denied.HTTP.Succeeded || denied.WriteOutcomeUnknown {
			t.Fatal("native denial became success or unknown", err)
		}
	}
}
