package sqlworkspace

import (
	"context"
	"github.com/nyakang/nyadb/internal/db/sqltext"
	"sync/atomic"
	"testing"
	"time"
)

func TestCompletionReadsReferencedFieldsWithoutDataDocumentAndRevokesLateReplies(t *testing.T) {
	generation := uint64(7)
	calls := make(chan string, 4)
	release := make(chan struct{})
	s := New(Dependencies{Connected: func(int64) bool { return true }, CompletionCatalog: func(int64, string) sqltext.CompletionCatalog {
		return sqltext.CompletionCatalog{Dialect: "sqlite", Namespace: "main"}
	}, CompletionSource: func(int64) CompletionSource {
		return CompletionSource{Generation: generation, Load: func(ctx context.Context, ns, name string) ([]string, error) {
			calls <- ns + "." + name
			select {
			case <-release:
				return []string{"id", "中文字段"}, nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}}
	}})
	defer s.CloseAll()
	d := s.Open(2, "SELECT a. FROM customers a", 1, "main", false)
	s.updateCompletionMetadata(d)
	time.Sleep(270 * time.Millisecond)
	s.Consume()
	if got := <-calls; got != "main.customers" {
		t.Fatal("wrong qualified reference", got)
	}
	generation++
	s.Consume()
	close(release)
	time.Sleep(270 * time.Millisecond)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		s.Consume()
		catalog := s.completionCatalog(d)
		if len(catalog.Objects) > 0 && len(catalog.Objects[0].Columns) == 2 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("completion did not publish current-generation fields")
}

func TestCompletionObjectCacheReusesSharedRelationAndClearsOnClose(t *testing.T) {
	var reads atomic.Int32
	s := New(Dependencies{Connected: func(int64) bool { return true }, CompletionCatalog: func(int64, string) sqltext.CompletionCatalog {
		return sqltext.CompletionCatalog{Dialect: "sqlite", Namespace: "main"}
	}, CompletionSource: func(int64) CompletionSource {
		return CompletionSource{Generation: 1, Catalog: "main", Load: func(context.Context, string, string) ([]string, error) { reads.Add(1); return []string{"id"}, nil }}
	}})
	d := s.Open(2, "SELECT * FROM a", 1, "main", false)
	load := func(want int) {
		s.updateCompletionMetadata(d)
		d.metadata.due = time.Time{}
		deadline := time.Now().Add(time.Second)
		for time.Now().Before(deadline) {
			s.Consume()
			if len(s.completionCatalog(d).Objects) == want {
				return
			}
			time.Sleep(time.Millisecond)
		}
		t.Fatal("completion did not settle")
	}
	load(1)
	d.Editor.Select(0, d.Editor.Buffer.Len())
	d.Editor.Insert("SELECT * FROM a JOIN b ON a.id=b.id")
	load(2)
	if reads.Load() != 2 {
		t.Fatal("shared object metadata was read twice", reads.Load())
	}
	s.CloseAll()
	if len(s.completionObjects) != 0 || len(s.completionCache) != 0 {
		t.Fatal("close retained metadata")
	}
}
