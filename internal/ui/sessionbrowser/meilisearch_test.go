package sessionbrowser

import (
	"github.com/nyakang/nyadb/internal/services/connections"
	"strings"
	"testing"
)

func TestRESTSessionStatusUsesOriginalProductLabel(t *testing.T) {
	s := New(Dependencies{})
	defer s.Close()
	for _, kind := range []connections.Kind{connections.Qdrant, connections.Meilisearch, connections.CouchDB, connections.Elasticsearch, connections.Easysearch, connections.Solr} {
		d, _ := connections.Describe(kind)
		if label := s.engineLabel(&Workspace{Kind: kind}); label != d.Label {
			t.Fatal(kind, label)
		}
		if label := s.engineStatus(&Workspace{Kind: kind}, 2); !strings.Contains(label, d.Label) {
			t.Fatal(label)
		}
	}
}
