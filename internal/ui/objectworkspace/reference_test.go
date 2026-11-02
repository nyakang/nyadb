package objectworkspace

import (
	"context"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
)

func TestObjectReferenceFreshDefinitionAndRevokedActiveScope(t *testing.T) {
	for _, change := range []string{"none", "switch", "target_generation", "close"} {
		t.Run(change, func(t *testing.T) {
			runtime, n := searchFixture()
			active := Session{ProfileID: 8, Connected: true, Generation: 2}
			target := Session{ProfileID: 7, Connected: true, Generation: 3}
			release, started := make(chan struct{}), make(chan struct{})
			runtime.Browse = func(context.Context, contract.NamespaceRef) ([]contract.DBObject, error) {
				close(started)
				<-release
				return []contract.DBObject{{Ref: n.Ref.Object("function", "f"), Name: "f", Kind: "函数", Definition: "CREATE FUNCTION f() RETURNS INT RETURN 42"}}, nil
			}
			opened := 0
			s := New(Dependencies{Session: func() Session { return active }, SessionFor: func(int64) Session { return target }, Capabilities: func(int64) contract.Capabilities { return contract.Capabilities{Metadata: true} }, SearchRuntime: func(int64) (ObjectSearchRuntime, error) { return runtime, nil }, OpenSearchObject: func(_ ui.Services, p int64, g uint64, e ObjectSearchEntry) {
				if p != 7 || g != 3 || e.Object.Definition == "" {
					t.Fatal("incomplete or wrong reference")
				}
				opened++
			}})
			defer s.CloseAll()
			s.ResolveObjectReference(ui.Services{}, 7, 3, n.Ref.Object("function", "f"), n.Label)
			<-started
			receipt := s.referenceRequest.results
			switch change {
			case "switch":
				active.ProfileID = 9
			case "target_generation":
				target.Generation++
			case "close":
				s.CloseAll()
			}
			close(release)
			if change == "none" {
				until := time.Now().Add(time.Second)
				for s.referenceRequest != nil && time.Now().Before(until) {
					s.consumeObjectReference(ui.Services{})
					time.Sleep(time.Millisecond)
				}
				if opened != 1 {
					t.Fatal("fresh reference did not open")
				}
			} else {
				select {
				case r := <-receipt:
					out := make(chan objectSearchResult, 1)
					out <- r
					if s.referenceRequest != nil {
						s.referenceRequest.results = out
					}
				case <-time.After(time.Second):
					t.Fatal("worker did not settle")
				}
				s.consumeObjectReference(ui.Services{})
				if opened != 0 {
					t.Fatal("stale reference opened new scope")
				}
			}
		})
	}
}
