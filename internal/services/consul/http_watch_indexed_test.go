package consul

import (
	"context"
	"net/http"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func TestHTTPWatchIndexedArchiveImmutableSelectionAndAcknowledgedStatus(t *testing.T) {
	s, _ := documentSession(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Consul-Index", "12")
		w.Write([]byte(`[{"Key":"watch/key","Value":"AP8=","Flags":18446744073709551615}]`))
	})
	l, err := s.ReserveKVHTTPWatch(101)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	r := testHTTPWatchRequest()
	r.MaxObservations = 1
	w := startHTTPWatch(t, l, r)
	settledHTTPWatch(t, w)
	if err := w.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	indexed := w.(contract.KVHTTPWatchIndexedRun)
	status := indexed.PollStatus()
	if status.Observations != 1 || !status.Ended || len(w.Poll().Observations) != 0 {
		t.Fatal("status did not consume only its original pending buffer")
	}
	o, ok := indexed.Observation(0)
	if !ok {
		t.Fatal("original ordinal missing")
	}
	o.HTTP.Body[0] = '!'
	o.HTTP.Entries[0].Value[0] = 9
	o.Matches[0] = 99
	again, ok := indexed.Observation(0)
	if !ok || again.HTTP.Body[0] != '[' || again.HTTP.Entries[0].Value[0] != 0 || again.Matches[0] != 0 {
		t.Fatal("selected copy mutated original archive")
	}
	for _, i := range []int{-1, 1, 999} {
		if _, ok := indexed.Observation(i); ok {
			t.Fatal("invalid ordinal accepted")
		}
	}
	if archive := l.(contract.KVHTTPWatchArchiveLease).RetainedWatch(); archive != w {
		t.Fatal("retained archive was replaced")
	}
}
