package consul

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func TestDocumentPresentationAndNamespaceRemainOriginalWithoutIO(t *testing.T) {
	s, calls := documentSession(t, func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("[]")) })
	before := calls.Load()
	p := s.KVRequestPresentation()
	builder := s.KVRequestBuilder()
	if p.Product != "Consul" || len(p.Methods) != 1 || p.Methods[0].Name != "HTTP" || p.Default.Method != contract.KVConsulHTTPRequestMethod {
		t.Fatal("missing native presentation")
	}
	p.Methods[0].Name = "foreign"
	p.Tools[0].Default.Key = "foreign"
	if len(s.KVRequestPresentation().Tools) != 6 || s.KVRequestPresentation().Tools[0].Default.Key == "foreign" {
		t.Fatal("structured presentation aliased original inputs")
	}
	if s.KVRequestPresentation().Methods[0].Name != "HTTP" || calls.Load() != before {
		t.Fatal("presentation mutated session or performed IO")
	}
	if s.Namespace("kv") != s.KVScope().Namespace || s.Namespace("dc1").Catalog != "" {
		t.Fatal("DC became SQL namespace")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Namespaces(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled metadata ignored")
	}
	if _, err := s.Browse(context.Background(), contract.NamespaceRef{ProfileID: 2, Catalog: "kv"}); !errors.Is(err, contract.ErrUnsupported) {
		t.Fatal("foreign namespace accepted")
	}
	s.Close()
	if _, err := builder.BuildKVRequest(contract.KVToolRead, contract.KVRequestToolInput{Key: "absolute/key"}); err != nil || calls.Load() != before {
		t.Fatal("pure builder made IO or depended on live session", err)
	}
	if _, err := s.Namespaces(context.Background()); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("closed namespace still available")
	}
}
