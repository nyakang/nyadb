package consul

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"reflect"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
)

const healthWatchCheckJSON = `{"Node":"original","CheckID":"original-check","Name":"原检查","Status":"passing","ServiceName":"原服务","Interval":18446744073709551615,"ModifyIndex":18446744073709551615,"future":{"null":null}}`

func healthWatchRequest() contract.KVHTTPWatchRequest {
	return contract.KVHTTPWatchRequest{Health: contract.KVHTTPHealthWatchTarget{Kind: "state", Name: "any"}, Wait: 100 * time.Millisecond, MaxObservations: 1, MaxBytes: 8 << 20}
}

func TestHTTPHealthWatchOriginalEndpointsOptionsImmutableSnapshotsAndIndependentUnknown(t *testing.T) {
	for _, kind := range []string{"state", "node", "checks", "service"} {
		t.Run(kind, func(t *testing.T) {
			var reads atomic.Int32
			s, calls := documentSession(t, func(w http.ResponseWriter, r *http.Request) {
				reads.Add(1)
				if r.Method != "GET" || r.URL.Query().Get("index") != "18446744073709551615" || r.URL.Query().Get("wait") != "100ms" || !r.URL.Query().Has("filter") || r.URL.Query().Get("filter") != "" || r.URL.Query().Get("near") != "_agent" || r.URL.Query().Has("recurse") {
					t.Error("original health options changed")
				}
				w.Header().Set("X-Consul-Results-Filtered-By-ACLs", "true")
				if kind == "service" {
					if r.URL.Query().Get("passing") != "false" {
						t.Error("explicit passing=false lost")
					}
					fmt.Fprint(w, `[{"Node":{"Node":"original"},"Service":{"ID":"original-service","Service":"原服务","Meta":{"a":"original"}},"Checks":[`+healthWatchCheckJSON+`]}]`)
				} else {
					fmt.Fprint(w, "["+healthWatchCheckJSON+"]")
				}
			})
			l, err := s.ReserveKVHTTPWatch(1)
			if err != nil {
				t.Fatal(err)
			}
			defer l.Close()
			writer, err := s.ReserveKV(1)
			if err != nil {
				t.Fatal(err)
			}
			defer writer.Close()
			writer.(*documentLease).writeUnknown.Store(true)
			r := healthWatchRequest()
			r.Health.Kind = kind
			r.Health.IncludeFilter = true
			r.Health.IncludeNear = true
			r.Health.Near = "_agent"
			r.StartIndex = math.MaxUint64
			switch kind {
			case "node":
				r.Health.Name = "original"
			case "checks", "service":
				r.Health.Name = "原服务"
			}
			if kind == "service" {
				r.Health.Passing = "false"
			}
			before := calls.Load()
			original := r
			prepared, err := l.Prepare(r)
			if err != nil || prepared.Snapshot() != original || calls.Load() != before {
				t.Fatal("health preparation changed scope/options or performed I/O", err)
			}
			r.Health.Name = "replacement"
			r.Health.Near = "replacement"
			if prepared.Snapshot() != original {
				t.Fatal("caller edit changed original capture")
			}
			foreign, _ := s.ReserveKVHTTPWatch(2)
			defer foreign.Close()
			if _, err := foreign.Start(context.Background(), prepared); !errors.Is(err, contract.ErrKVPlanUsed) || calls.Load() != before {
				t.Fatal("foreign preparation dispatched", err)
			}
			run, err := l.Start(context.Background(), prepared)
			if err != nil {
				t.Fatal(err)
			}
			defer run.Close()
			got := settledHTTPWatch(t, run)
			if got.Request != original || got.Terminal.Reason != "observation-limit" || len(got.Observations) != 1 || reads.Load() != 1 {
				t.Fatal("original bounded health run changed", got.Terminal)
			}
			o := got.Observations[0]
			if !o.ProjectionComplete || o.HTTP.Health == nil || !o.HTTP.Health.ProjectionComplete || o.HTTP.Health.Complete || len(o.Matches) != 0 || o.IndexRelation != "unchanged" || o.IndexReset || *o.HTTP.Metadata.Index != math.MaxUint64 {
				t.Fatal("ACL observations or original unsigned index lost")
			}
			var check contract.KVHTTPHealthCheck
			if kind == "service" {
				check = o.HTTP.Health.Instances[0].Checks[0]
			} else {
				check = o.HTTP.Health.Checks[0]
			}
			if check.ModifyIndex != math.MaxUint64 || check.Interval.Nanoseconds == nil || *check.Interval.Nanoseconds != math.MaxUint64 || !bytes.Contains(o.HTTP.Body, []byte(`"future"`)) {
				t.Fatal("original health types/body changed")
			}
			got.Observations[0].HTTP.Body[0] = 'x'
			*got.Observations[0].HTTP.Metadata.Index = 1
			if kind == "service" {
				got.Observations[0].HTTP.Health.Instances[0].Service.Meta["a"] = "replacement"
				*got.Observations[0].HTTP.Health.Instances[0].Checks[0].Interval.Nanoseconds = 0
			} else {
				*got.Observations[0].HTTP.Health.Checks[0].Interval.Nanoseconds = 0
			}
			fresh := run.Poll().Observations[0]
			if fresh.HTTP.Body[0] != '[' || *fresh.HTTP.Metadata.Index != math.MaxUint64 {
				t.Fatal("snapshot changed original archive")
			}
			if kind == "service" {
				if fresh.HTTP.Health.Instances[0].Service.Meta["a"] != "original" || *fresh.HTTP.Health.Instances[0].Checks[0].Interval.Nanoseconds != math.MaxUint64 {
					t.Fatal("nested service snapshot not owned")
				}
			} else if *fresh.HTTP.Health.Checks[0].Interval.Nanoseconds != math.MaxUint64 {
				t.Fatal("nested check duration not owned")
			}
			if !writer.(*documentLease).writeUnknown.Load() {
				t.Fatal("health read erased original unknown write")
			}
		})
	}
}

func TestHTTPHealthWatchIndexResetRetainsNativeEvidenceAndNeverInventsHistory(t *testing.T) {
	indices := []uint64{math.MaxUint64, 0, 1, 2, 1, 3}
	requests := make(chan uint64, len(indices))
	var reads atomic.Int32
	s, _ := documentSession(t, func(w http.ResponseWriter, r *http.Request) {
		i := int(reads.Add(1)) - 1
		index, err := strconv.ParseUint(r.URL.Query().Get("index"), 10, 64)
		if err != nil {
			t.Error(err)
		}
		requests <- index
		w.Header().Set("X-Consul-Index", strconv.FormatUint(indices[i], 10))
		fmt.Fprint(w, "["+healthWatchCheckJSON+"]")
	})
	l, _ := s.ReserveKVHTTPWatch(1)
	defer l.Close()
	r := healthWatchRequest()
	r.MaxObservations = len(indices)
	r.MaxBytes = 16 << 20
	run := startHTTPWatch(t, l, r)
	got := settledHTTPWatch(t, run)
	if len(got.Observations) != len(indices) || got.Terminal.Reason != "observation-limit" {
		t.Fatal("reset shortened or restarted original run", got.Terminal)
	}
	wantRequests := []uint64{0, math.MaxUint64, 1, 1, 2, 1}
	for i, want := range wantRequests {
		if actual := <-requests; actual != want {
			t.Fatal("health blocking cursor changed", i, actual, want)
		}
	}
	wantRelations := []string{"initial", "zero", "unchanged", "advanced", "backward", "advanced"}
	for i, o := range got.Observations {
		if *o.HTTP.Metadata.Index != indices[i] || o.IndexRelation != wantRelations[i] || o.IndexReset != (i == 1 || i == 4) || o.ReceivedAt.IsZero() {
			t.Fatal("native reset evidence changed", i, o.IndexRelation)
		}
	}
	if run.Request() != r || !reflect.DeepEqual(got.Request, r) {
		t.Fatal("cursor updates mutated authored start index")
	}
}

func TestHTTPHealthWatchInvalidMixedTargetsRejectedBeforeNativeIO(t *testing.T) {
	s, calls := documentSession(t, func(http.ResponseWriter, *http.Request) { t.Error("invalid health target dispatched") })
	l, _ := s.ReserveKVHTTPWatch(1)
	defer l.Close()
	before := calls.Load()
	for _, change := range []func(*contract.KVHTTPWatchRequest){
		func(r *contract.KVHTTPWatchRequest) { r.Target = "key" }, func(r *contract.KVHTTPWatchRequest) { r.ObservationPrefix = ""; r.Prefix = true },
		func(r *contract.KVHTTPWatchRequest) { r.Health.Kind = "agent" }, func(r *contract.KVHTTPWatchRequest) { r.Health.Name = "maintenance" },
		func(r *contract.KVHTTPWatchRequest) { r.Health.Passing = "true" }, func(r *contract.KVHTTPWatchRequest) { r.Health.Filter = "undeclared" },
		func(r *contract.KVHTTPWatchRequest) { r.Health.Near = "undeclared" }, func(r *contract.KVHTTPWatchRequest) { r.Health.Kind = "node"; r.Health.Name = "bad//name" },
		func(r *contract.KVHTTPWatchRequest) {
			r.Health.Kind = "service"
			r.Health.Name = "original"
			r.Health.Passing = "null"
		},
	} {
		r := healthWatchRequest()
		change(&r)
		if _, err := l.Prepare(r); err == nil {
			t.Fatal("invalid or mixed target accepted", r)
		}
	}
	r := testHTTPWatchRequest()
	r.Health.Name = "ignored-health-field"
	if _, err := l.Prepare(r); err == nil || calls.Load() != before {
		t.Fatal("unused health authority smuggled into KV watch")
	}
}

func TestHTTPHealthWatchOriginalReadFailureAndProjectionBudgetStopWithoutRetry(t *testing.T) {
	for _, mode := range []string{"denied", "partial", "missing-index", "invalid-index", "projection", "overflow"} {
		t.Run(mode, func(t *testing.T) {
			var reads atomic.Int32
			s, _ := documentSession(t, func(w http.ResponseWriter, r *http.Request) {
				reads.Add(1)
				switch mode {
				case "denied":
					w.WriteHeader(403)
					fmt.Fprint(w, "original denied")
					return
				case "missing-index":
					w.Header().Del("X-Consul-Index")
				case "invalid-index":
					w.Header().Set("X-Consul-Index", "18446744073709551616")
				case "projection":
					fmt.Fprint(w, `[{"Node":"original","ModifyIndex":18446744073709551616}]`)
					return
				case "partial":
					conn, buf, err := w.(http.Hijacker).Hijack()
					if err != nil {
						t.Error(err)
						return
					}
					defer conn.Close()
					fmt.Fprintf(buf, "HTTP/1.1 200 OK\r\nContent-Length: 999\r\nX-Consul-Index: 9\r\n\r\n[")
					buf.Flush()
					return
				}
				fmt.Fprint(w, "["+healthWatchCheckJSON+"]")
			})
			l, _ := s.ReserveKVHTTPWatch(1)
			defer l.Close()
			r := healthWatchRequest()
			r.MaxObservations = 3
			if mode == "overflow" {
				r.MaxBytes = 1
			}
			run := startHTTPWatch(t, l, r)
			got := settledHTTPWatch(t, run)
			if reads.Load() != 1 || len(got.Observations) != 1 || !got.Observations[0].HTTP.Ended || len(got.Observations[0].HTTP.Body) == 0 {
				t.Fatal("failed read retried or original receipt lost", got.Terminal)
			}
			o := got.Observations[0]
			if o.HTTP.WriteOutcomeUnknown {
				t.Fatal("health read acquired a write outcome")
			}
			if mode == "projection" && (!o.HTTP.Acknowledged || !o.HTTP.Captured || o.HTTP.Health != nil || o.ProjectionComplete) {
				t.Fatal("projection failure replaced original complete ACK")
			}
			if mode == "overflow" && (!got.TerminalOverflow || got.Terminal.Reason != "byte-limit" || !o.ProjectionComplete || o.HTTP.Health == nil) {
				t.Fatal("complete terminal overflow evidence evicted")
			}
		})
	}
}

func TestHTTPHealthWatchCancellationAwaitsOriginalExecutorAndRecoveryRetainsArchive(t *testing.T) {
	s, _ := documentSession(t, func(http.ResponseWriter, *http.Request) { t.Error("unexpected native dispatch") })
	entered, canceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	s.opener = func(context.Context) (*Client, OpenEvidence, error) {
		c := newCapturedClient(s.cfg, nil, "")
		c.cache = false
		c.http.Transport = watchRoundTripper(func(r *http.Request) (*http.Response, error) {
			close(entered)
			<-r.Context().Done()
			close(canceled)
			<-release
			return nil, r.Context().Err()
		})
		return c, s.evidence, nil
	}
	l, _ := s.ReserveKVHTTPWatch(1)
	defer l.Close()
	r := healthWatchRequest()
	run := startHTTPWatch(t, l, r)
	<-entered
	ended := make(chan error, 1)
	go func() { ended <- run.Stop(context.Background()) }()
	<-canceled
	select {
	case <-ended:
		t.Fatal("health stop claimed ACK before original executor returned")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	if err := <-ended; err != nil {
		t.Fatal(err)
	}
	got := settledHTTPWatch(t, run)
	if len(got.Observations) != 1 || !got.Observations[0].HTTP.Canceled || !got.Observations[0].HTTP.Ended {
		t.Fatal("original canceled receipt lost")
	}
	before := got.Clone()
	recovered, err := s.RecoverKVHTTPWatch(l)
	if err != nil || recovered.Lease == nil || recovered.Watch == nil {
		t.Fatal("evidence-only health recovery failed", err)
	}
	defer recovered.Lease.Close()
	if !reflect.DeepEqual(*recovered.Watch, before) || !reflect.DeepEqual(*recovered.Lease.LastWatch(), before) {
		t.Fatal("health recovery replayed or changed settled archive")
	}
	if err := run.Close(); err != nil {
		t.Fatal(err)
	}
	if recovered.Lease.LastWatch() == nil {
		t.Fatal("late original Close released transferred archive")
	}
}
