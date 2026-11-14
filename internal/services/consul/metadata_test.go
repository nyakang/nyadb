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
	if len(s.KVRequestPresentation().Tools) != 30 || s.KVRequestPresentation().Tools[0].Default.Key == "foreign" {
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

func TestPublicDataMetadataAndReservationKeepOriginalPreviewScopeWithoutIO(t *testing.T) {
	f := &dataFixture{}
	s := dataTestSession(t, "original//prefix/", f)
	before := f.reads.Load()
	objects, err := s.Browse(context.Background(), s.KVScope().Namespace)
	object := s.KVScope().Namespace.Object("keyspace", "keys")
	if err != nil || len(objects) != 1 || objects[0].Ref != object || objects[0].Kind != "键" {
		t.Fatal("original keyspace metadata missing", err)
	}
	structure, err := s.TableMetadata(context.Background(), object)
	if err != nil || structure.Object != object || len(structure.Columns) != 7 || structure.Columns[2].DataType != "uint64" {
		t.Fatal("original typed key structure missing", err)
	}
	if structure.Columns[2].Generated || !structure.Columns[3].Generated || !structure.Columns[4].Generated || !structure.Columns[5].Generated {
		t.Fatal("user-supplied flags were conflated with generated native indices")
	}
	lease, err := s.ReserveDataDocument(object)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	runtime := lease.Runtime()
	if f.reads.Load() != before || runtime.AdvancedLoader == nil || runtime.Count == nil || runtime.Chunk == nil || runtime.Exporter == nil || runtime.Saver == nil || runtime.PreviewEdits == nil || runtime.SubmitEdits == nil || runtime.Transactions.Begin != nil || !runtime.Capabilities.EditableTables || runtime.Capabilities.Query {
		t.Fatal("original preview publication made I/O or lost bound capabilities")
	}
	if _, err := s.TableMetadata(context.Background(), object.Namespace().Object("table", "keys")); !errors.Is(err, contract.ErrUnsupported) {
		t.Fatal("unqualified structure kind accepted", err)
	}
	structure.Columns[0].Name = "foreign"
	fresh, err := s.TableMetadata(context.Background(), object)
	if err != nil || fresh.Columns[0].Name != "key" {
		t.Fatal("structure metadata was shared")
	}
}
