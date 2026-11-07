package elasticsearch

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func fakeDataDocument(t *testing.T) (*Session, *dataLease, *atomic.Int32) {
	t.Helper()
	cleanups := &atomic.Int32{}
	c := fakeClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/_resolve/"):
			_, _ = io.WriteString(w, `{"indices":[{"name":"own","attributes":["open"]}],"aliases":[],"data_streams":[]}`)
		case strings.Contains(r.URL.Path, "/_settings/"):
			_, _ = io.WriteString(w, `{"own":{"settings":{"index.uuid":"original-uuid"}}}`)
		case r.URL.Path == "/own/_pit":
			_, _ = io.WriteString(w, `{"id":"original-pit"}`)
		case r.URL.Path == "/_search":
			_, _ = io.WriteString(w, `{"pit_id":"latest-pit","timed_out":false,"_shards":{"total":1,"successful":1,"failed":0},"hits":{"total":{"value":2,"relation":"eq"},"hits":[{"_index":"own","_id":"one","_seq_no":9007199254740993,"_primary_term":1,"_source":{"n":9007199254740993},"sort":[9007199254740993]},{"_index":"own","_id":"two","_seq_no":2,"_primary_term":1,"_source":{"n":2},"sort":[9007199254740994]}]}}`)
		case r.URL.Path == "/_pit":
			var body struct {
				ID string `json:"id"`
			}
			if json.NewDecoder(r.Body).Decode(&body) != nil || body.ID != "latest-pit" {
				t.Error("cleanup did not use latest original PIT")
			}
			cleanups.Add(1)
			_, _ = io.WriteString(w, `{"succeeded":true,"num_freed":1}`)
		default:
			t.Error("unexpected native request", r.Method, r.URL.Path)
			w.WriteHeader(400)
		}
	})
	raw, err := OpenSession(context.Background(), sessionProfile(t, c.config), "")
	if err != nil {
		t.Fatal(err)
	}
	s := raw.(*Session)
	t.Cleanup(func() { _ = s.Close() })
	l := nativeDataLease(t, s, "index", "own")
	return s, l, cleanups
}
func TestElasticsearchDataDocumentCloseWaitsHTTPAndReleasesUnpublishedPIT(t *testing.T) {
	s, l, cleanups := fakeDataDocument(t)
	o := contract.DataOptions{Object: l.object, Limit: 1}
	page, err := l.Runtime().AdvancedLoader(context.Background(), s.ProfileID(), "nyadb", "own", o)
	if err != nil {
		t.Fatal(err)
	}
	// The page is not delivered to any UI consumer. Its resource still belongs to l.
	started, canceled, ack := make(chan struct{}), make(chan struct{}), make(chan struct{})
	original := l.client.http.Transport
	l.client.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/_search" {
			return original.RoundTrip(r)
		}
		close(started)
		<-r.Context().Done()
		close(canceled)
		<-ack
		return nil, r.Context().Err()
	})
	o.Offset = 1
	o.After = page.NextCursor
	result := make(chan error, 1)
	go func() {
		_, e := l.Runtime().AdvancedLoader(context.Background(), s.ProfileID(), "nyadb", "own", o)
		result <- e
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("read did not begin")
	}
	closed := make(chan error, 2)
	go func() { closed <- s.Close() }()
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("Close did not revoke original executor")
	}
	go func() { closed <- s.Close() }()
	select {
	case <-closed:
		t.Fatal("Close returned before original acknowledgement")
	default:
	}
	if cleanups.Load() != 0 {
		t.Fatal("PIT released before executor finished")
	}
	close(ack)
	select {
	case e := <-result:
		if e == nil {
			t.Fatal("canceled read became success")
		}
	case <-time.After(time.Second):
		t.Fatal("executor stalled")
	}
	for i := 0; i < 2; i++ {
		select {
		case e := <-closed:
			if e != nil {
				t.Fatal(e)
			}
		case <-time.After(time.Second):
			t.Fatal("Close did not settle")
		}
	}
	if cleanups.Load() != 1 || l.client != nil || l.cursor != nil || len(l.members) != 0 || l.key != [32]byte{} {
		t.Fatal("unpublished resource or identity cache retained")
	}
	if _, err = l.Runtime().AdvancedLoader(context.Background(), s.ProfileID(), "nyadb", "own", o); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("closed resource rebound", err)
	}
}
func TestElasticsearchDataDocumentRejectsModifiedBoundariesAndRowIdentitiesBeforeIO(t *testing.T) {
	s, l, _ := fakeDataDocument(t)
	o := contract.DataOptions{Object: l.object, Limit: 1}
	page, err := l.page(context.Background(), s.ProfileID(), "nyadb", "own", o)
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	original := l.client.http.Transport
	l.client.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) { calls.Add(1); return original.RoundTrip(r) })
	bad := *page.NextCursor
	bad.Values = [][]byte{append([]byte(nil), bad.Values[0]...), []byte("9007199254740994")}
	o.Offset = 1
	o.After = &bad
	if _, err = l.page(context.Background(), s.ProfileID(), "nyadb", "own", o); err == nil || calls.Load() != 0 {
		t.Fatal("modified boundary reached original executor")
	}
	r := *page.RowRefs[0]
	r.Values = append([][]byte(nil), r.Values...)
	r.Values[1] = []byte("other")
	if _, err = l.row(context.Background(), s.ProfileID(), "nyadb", "own", r); err == nil {
		t.Fatal("modified row identity accepted")
	}
	unsupported := []contract.DataOptions{{SortColumn: "_id"}, {FilterColumn: "document", FilterMode: "equal", FilterValue: `{"term":{"n":1}}`}, {Offset: 1}, {FilterValue: "ignored"}}
	for _, v := range unsupported {
		v.Object = l.object
		v.Limit = 1
		if _, err = l.page(context.Background(), s.ProfileID(), "nyadb", "own", v); err == nil {
			t.Fatal("unsupported semantics accepted")
		}
	}
}

func TestElasticsearchDataDocumentReservationsAreLazyBoundedAndReleaseCapacity(t *testing.T) {
	s, first, cleanups := fakeDataDocument(t)
	for i := 1; i < 32; i++ {
		if _, err := s.ReserveDataDocument(first.object); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.ReserveDataDocument(first.object); !errors.Is(err, ErrBounds) {
		t.Fatal("unbounded document reservations", err)
	}
	for l := range s.datas {
		if l.client != nil || l.cursor != nil {
			t.Fatal("reservation performed native I/O")
		}
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReserveDataDocument(first.object); err != nil {
		t.Fatal("closed document retained reservation capacity", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if len(s.datas) != 0 || cleanups.Load() != 0 {
		t.Fatal("unopened documents allocated or retained PIT resources")
	}
}
