package qdrant

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func unitManagementLease(t *testing.T, handler http.HandlerFunc) (*Session, *ManagementLease) {
	t.Helper()
	server := sessionServer(t, handler)
	s := testSession(t, connections.QdrantConfig{Endpoint: server.URL}, "")
	l, err := s.ReserveManagementDocument(1)
	if err != nil {
		t.Fatal(err)
	}
	return s, l
}
func TestQdrantManagementObservationOriginalProofPreviewReplacementAndBounds(t *testing.T) {
	var requests atomic.Int32
	s, l := unitManagementLease(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Path == "/aliases" {
			fmt.Fprint(w, `{"status":"ok","result":{"aliases":[]}}`)
			return
		}
		fmt.Fprint(w, sessionCollection)
	})
	o, err := l.Read(context.Background(), []string{"owned"})
	if err != nil || len(o.Token) != 64 || len(o.Configs) != 1 || len(o.Responses) != 3 {
		t.Fatal("original signed management observation", err)
	}
	first, err := l.PrepareCollection(o, "owned", "collection_delete", nil, 2)
	if err != nil {
		t.Fatal(err)
	}
	calls := requests.Load()
	second, err := l.PrepareCollection(o, "owned", "collection_update", []byte(`{"metadata":{"n":9007199254740993}}`), 2)
	if err != nil || requests.Load() != calls {
		t.Fatal("prepare performed I/O", err)
	}
	if _, err = l.Submit(context.Background(), first); !errors.Is(err, ErrManagementUsed) || requests.Load() != calls {
		t.Fatal("replaced preview dispatched", err)
	}
	other, err := s.ReserveManagementDocument(2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = other.Submit(context.Background(), second); !errors.Is(err, ErrManagementUsed) || requests.Load() != calls {
		t.Fatal("foreign original lease accepted a preview", err)
	}
	for _, mutate := range []func(*ManagementObservation){
		func(o *ManagementObservation) { o.Configs["owned"][0] = 'x' },
		func(o *ManagementObservation) { o.Responses[0].Response.Body[0] = 'x' },
		func(o *ManagementObservation) { o.ProfileID++ },
		func(o *ManagementObservation) { o.Targets[0] = "other" },
	} {
		bad := cloneManagementObservation(o)
		mutate(&bad)
		if _, err = l.PrepareCollection(bad, "owned", "collection_delete", nil, 2); !errors.Is(err, contract.ErrNamespaceChanged) {
			t.Fatal("forged original observation accepted", err)
		}
	}
	bad := cloneManagementObservation(o)
	bad.Configs["owned"] = []byte(strings.Repeat("x", requestLimit+1))
	if _, err = l.PrepareCollection(bad, "owned", "collection_delete", nil, 2); !errors.Is(err, ErrBounds) {
		t.Fatal("unbounded forged source reached signing", err)
	}
	o.Configs["owned"][0] = 'x'
	if second.before.Configs["owned"][0] != '{' {
		t.Fatal("caller mutation escaped original captured plan")
	}
	if _, err = l.PrepareCollection(second.before, "owned", "collection_create", []byte(`{}`), 2); !errors.Is(err, contract.ErrNamespaceExists) {
		t.Fatal("existing collection accepted as new", err)
	}
	l.Close()
	l.Close()
	if l.observation != nil || l.pending != nil || l.key != [32]byte{} {
		t.Fatal("close retained original proof")
	}
	if _, err = l.PrepareCollection(second.before, "owned", "collection_delete", nil, 2); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("closed lease prepared new work", err)
	}
}

func TestQdrantManagementMetadataEqualityPreservesNativePatchSemantics(t *testing.T) {
	if managementEqual([]byte(`null`), []byte(`{}`)) || managementEqual([]byte(`{}`), []byte(`{"old":true}`)) || managementEqual([]byte(`9007199254740993`), []byte(`9007199254740992`)) {
		t.Fatal("native metadata or exact integer semantics collapsed")
	}
	if !managementEqual([]byte(`1.2300e-20`), []byte(`1.23e-20`)) || !managementSubset([]byte(`{"m":24}`), []byte(`{"m":24,"ef_construct":100}`)) {
		t.Fatal("native number/default parameter normalization rejected")
	}
	for _, tc := range []struct {
		name, action, before, patch, matched, diverged string
	}{
		{"empty retains", "collection_update", `{"old":true}`, `{}`, `{"old":true}`, `{}`},
		{"null deletes", "collection_update", `{"old":true,"n":9007199254740993}`, `{"old":null}`, `{"n":9007199254740993}`, `{"old":null,"n":9007199254740993}`},
		{"nested replaces", "collection_update", `{"nested":{"old":true},"keep":1}`, `{"nested":{"new":true,"null":null}}`, `{"nested":{"new":true,"null":null},"keep":1}`, `{"nested":{"old":true,"new":true,"null":null},"keep":1}`},
		{"first payload preserves null", "collection_update", `null`, `{"n":null}`, `{"n":null}`, `{}`},
		{"optional null retains", "collection_update", `{"old":true}`, `null`, `{"old":true}`, `null`},
		{"optional absent retains", "collection_update", `null`, `null`, `null`, `{}`},
		{"create preserves null", "collection_create", `null`, `{"n":null}`, `{"n":null}`, `{}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := PrepareCollectionManagement("owned", tc.action, []byte(`{"metadata":`+tc.patch+`,"on_disk_payload":false}`), 2)
			if tc.action == "collection_update" {
				p, err = PrepareCollectionManagement("owned", tc.action, []byte(`{"metadata":`+tc.patch+`,"params":{"on_disk_payload":false}}`), 2)
			}
			if err != nil {
				t.Fatal(err)
			}
			before := []byte(`{"metadata":` + tc.before + `}`)
			checks := managementConfigMatches(p, before, []byte(`{"params":{"on_disk_payload":false},"metadata":`+tc.matched+`}`))
			if !checks["metadata"] {
				t.Fatal("native metadata result rejected", checks)
			}
			if checks = managementConfigMatches(p, before, []byte(`{"metadata":`+tc.diverged+`}`)); checks["metadata"] {
				t.Fatal("diverged metadata accepted", checks)
			}
		})
	}
}

func TestQdrantManagementReadCapturesTargetsBeforeHandshakeAndPreparationNeverWaits(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var block atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/":
			if block.CompareAndSwap(true, false) {
				close(entered)
				<-release
			}
			fmt.Fprint(w, unitIdentity)
		case "/collections":
			fmt.Fprint(w, sessionCollections)
		case "/aliases":
			fmt.Fprint(w, `{"status":"ok","result":{"aliases":[]}}`)
		case "/collections/owned":
			fmt.Fprint(w, sessionCollection)
		default:
			t.Error("caller changed captured namespace", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(server.Close)
	s := testSession(t, connections.QdrantConfig{Endpoint: server.URL}, "")
	l, err := s.ReserveManagementDocument(1)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	block.Store(true)
	targets := []string{"owned"}
	type readResult struct {
		o   ManagementObservation
		err error
	}
	done := make(chan readResult, 1)
	go func() { o, err := l.Read(context.Background(), targets); done <- readResult{o, err} }()
	waitSessionSignal(t, entered)
	targets[0] = "caller_changed"
	busy := make(chan error, 1)
	go func() {
		_, err := l.PrepareCollection(ManagementObservation{}, "owned", "collection_delete", nil, 2)
		if !errors.Is(err, ErrBusy) {
			busy <- fmt.Errorf("collection preparation did not reject busy operation: %w", err)
			return
		}
		_, err = l.PrepareAliases(ManagementObservation{}, []AliasChange{{Action: "delete_alias", Alias: "alias"}}, 2)
		if !errors.Is(err, ErrBusy) {
			busy <- fmt.Errorf("alias preparation did not reject busy operation: %w", err)
			return
		}
		busy <- nil
	}()
	select {
	case err := <-busy:
		close(release)
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("pure preparation waited for active network operation")
	}
	r := <-done
	if r.err != nil || len(r.o.Targets) != 1 || r.o.Targets[0] != "owned" || len(r.o.Configs["owned"]) == 0 {
		t.Fatal("read did not capture original targets before handshake", r.err, r.o.Targets)
	}
}

func TestQdrantManagementReadBoundsRetainOriginalLastResponseWithoutProof(t *testing.T) {
	_, l := unitManagementLease(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/aliases" {
			fmt.Fprint(w, `{"status":"ok","result":{"aliases":[]}}`)
			return
		}
		fmt.Fprintf(w, `{"status":"ok","result":{"status":"green","config":{"metadata":{"large":"%s"}}}}`, strings.Repeat("x", 9<<20))
	})
	o, err := l.Read(context.Background(), []string{"owned"})
	if !errors.Is(err, ErrBounds) || o.Token != "" || l.observation != nil || len(o.Responses) != 3 {
		t.Fatal("bounded incomplete observation became valid proof", err)
	}
	last := o.Responses[2]
	retained := 0
	for _, r := range o.Responses {
		retained += len(r.Response.Body)
	}
	if !last.BodyTruncated || last.Response.Status != 200 || last.Response.ContentType != "application/json" || last.OriginalBodyBytes <= len(last.Response.Body) || len(last.Response.Body) == 0 || retained != requestLimit {
		t.Fatal("bounded original response metadata/prefix discarded")
	}
}
