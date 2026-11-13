package consul

import (
	"bytes"
	"context"
	"errors"
	"net/url"
	"os"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func nativeHealthWatchRequest(kind, name string) contract.KVHTTPWatchRequest {
	return contract.KVHTTPWatchRequest{Health: contract.KVHTTPHealthWatchTarget{Kind: kind, Name: name}, Wait: 100 * time.Millisecond, MaxObservations: 1, MaxBytes: 32 << 20}
}

func TestConsulNativeHealthWatchOriginalEndpointsBlockingChangeAndCompleteSnapshots(t *testing.T) {
	base, cfg, token := nativeClient(t)
	service, ids := nativeHealthFixture(t, base)
	s, err := OpenNativeSession(context.Background(), 3201, cfg, token)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for i, target := range []struct{ kind, name, passing string }{{"state", "any", ""}, {"node", s.KVScope().HTTP.AgentName, ""}, {"checks", service, ""}, {"service", service, ""}, {"service", service, "false"}, {"service", service, "true"}} {
		t.Run(target.kind+"-"+target.passing, func(t *testing.T) {
			l, err := s.ReserveKVHTTPWatch(uint64(i + 1))
			if err != nil {
				t.Fatal(err)
			}
			defer l.Close()
			r := nativeHealthWatchRequest(target.kind, target.name)
			r.Health.Passing = target.passing
			r.Health.IncludeNear = true
			r.Health.Near = "_agent"
			if target.kind == "state" || target.kind == "node" {
				r.Health.IncludeFilter = true
				r.Health.Filter = "ServiceName == `" + service + "`"
			}
			prepared, err := l.Prepare(r)
			if err != nil {
				t.Fatal(err)
			}
			run, err := l.Start(context.Background(), prepared)
			if err != nil {
				t.Fatal(err)
			}
			defer run.Close()
			got := settledHTTPWatch(t, run)
			if got.Request != r || len(got.Observations) != 1 || got.Terminal.Reason != "observation-limit" {
				t.Fatal("original health endpoint run changed", got.Terminal)
			}
			o := got.Observations[0]
			h := o.HTTP.Health
			if !o.ProjectionComplete || h == nil || !h.Complete || !o.HTTP.Captured || !o.HTTP.EOF || !o.HTTP.Acknowledged || o.HTTP.Metadata.Index == nil || len(o.Matches) != 0 {
				t.Fatal("original health response/ACL boundary lost")
			}
			rows := len(h.Checks) + len(h.Instances)
			want := 2
			if target.passing == "true" {
				want = 1
			}
			if rows != want {
				t.Fatal("native endpoint/passing semantics changed", rows, want)
			}
			if url.Values(o.HTTP.Request.Query).Get("index") != "0" || url.Values(o.HTTP.Request.Query).Get("wait") != "100ms" || url.Values(o.HTTP.Request.Query).Get("near") != "_agent" {
				t.Fatal("captured original read options changed")
			}
			if target.kind == "service" && target.passing == "" && url.Values(o.HTTP.Request.Query).Has("passing") {
				t.Fatal("omitted native passing became explicit")
			}
			clone := got.Clone()
			clone.Observations[0].HTTP.Body[0] = 'x'
			*clone.Observations[0].HTTP.Metadata.Index = 0
			fresh := run.Poll().Observations[0]
			if fresh.HTTP.Body[0] != '[' || *fresh.HTTP.Metadata.Index == 0 {
				t.Fatal("inspection modified original health archive")
			}
		})
	}
	l, err := s.ReserveKVHTTPWatch(100)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	r := nativeHealthWatchRequest("checks", service)
	r.MaxObservations = 16
	run := startHTTPWatch(t, l, r)
	deadline := time.Now().Add(10 * time.Second)
	for len(run.Snapshot().Observations) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("initial health snapshot missing")
		}
		time.Sleep(time.Millisecond)
	}
	initial := run.Snapshot().Observations[0]
	nativeHealthFixturePUT(t, base, "/v1/agent/check/update/"+url.PathEscape(ids[0]+"-check"), map[string]string{"Status": "critical", "Output": "changed health watch 中文"})
	found := false
	for !found {
		for _, o := range run.Snapshot().Observations {
			if o.HTTP.Health == nil {
				continue
			}
			for _, c := range o.HTTP.Health.Checks {
				if c.CheckID == ids[0]+"-check" && c.Status == "critical" && c.Output == "changed health watch 中文" {
					found = true
					if *o.HTTP.Metadata.Index <= *initial.HTTP.Metadata.Index {
						t.Fatal("native catalog change did not advance original blocking index")
					}
				}
			}
		}
		if run.Snapshot().Ended && !found || time.Now().After(deadline) {
			t.Fatal("native health change did not reach bounded watch")
		}
		time.Sleep(time.Millisecond)
	}
	if err := run.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := settledHTTPWatch(t, run)
	if !got.Ended || len(got.Observations) < 2 || got.Request != r || len(initial.HTTP.Body) == 0 {
		t.Fatal("native original watch evidence lost")
	}
}

func TestConsulNativeHealthWatchProcessedReadFaultsOriginalAcknowledgementAndRecovery(t *testing.T) {
	base, cfg, token := nativeClient(t)
	service, _ := nativeHealthFixture(t, base)
	for _, mode := range []string{"lost", "partial", "http500", "http408", "foreign", "null", "overflow", "cancel", "retire", "close"} {
		t.Run(mode, func(t *testing.T) {
			relay, calls, processed := nativeHealthProjectionRelay(t, cfg, token, service, mode)
			s, err := OpenNativeSession(context.Background(), 3202, relay, token)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			l, err := s.ReserveKVHTTPWatch(1)
			if err != nil {
				t.Fatal(err)
			}
			defer l.Close()
			r := nativeHealthWatchRequest("checks", service)
			r.MaxObservations = 3
			run := startHTTPWatch(t, l, r)
			wait(t, processed)
			switch mode {
			case "cancel":
				if err := run.Stop(context.Background()); err != nil {
					t.Fatal(err)
				}
			case "retire":
				s.Close()
			case "close":
				l.Close()
			}
			got := settledHTTPWatch(t, run)
			if calls.Load() != 1 {
				t.Fatal("failed original health read replayed")
			}
			if mode == "close" {
				if len(got.Observations) != 0 {
					t.Fatal("closed original watch retained cache")
				}
				return
			}
			if len(got.Observations) != 1 || !got.Observations[0].HTTP.Ended || got.Observations[0].ProjectionComplete || got.Observations[0].HTTP.Health != nil || got.Observations[0].HTTP.WriteOutcomeUnknown {
				t.Fatal("faulted health snapshot promoted or lost", got.Terminal)
			}
			o := got.Observations[0]
			if mode == "foreign" || mode == "null" || mode == "overflow" {
				if !o.HTTP.Captured || !o.HTTP.EOF || !o.HTTP.Acknowledged || !o.HTTP.Succeeded || got.Terminal.Reason != "projection-incomplete" {
					t.Fatal("optional projection changed native successful receipt")
				}
			}
			owner := s
			if mode == "retire" {
				owner, err = OpenNativeSession(context.Background(), 3202, relay, token)
				if err != nil {
					t.Fatal(err)
				}
				defer owner.Close()
			}
			recovered, err := owner.RecoverKVHTTPWatch(l)
			if err != nil || recovered.Watch == nil || recovered.Lease == nil {
				t.Fatal("original health archive recovery failed", err)
			}
			defer recovered.Lease.Close()
			if calls.Load() != 1 || !reflect.DeepEqual(*recovered.Watch, got) {
				t.Fatal("health recovery executed or changed original receipts")
			}
			if err := run.Close(); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(*recovered.Lease.LastWatch(), got) {
				t.Fatal("late original Close released transferred health archive")
			}
			if err := recovered.Lease.ClearObservations(); err != nil {
				t.Fatal(err)
			}
			r.MaxObservations = 1
			next := startHTTPWatch(t, recovered.Lease, r)
			fresh := settledHTTPWatch(t, next)
			if calls.Load() != 2 || len(fresh.Observations) != 1 || !fresh.Observations[0].ProjectionComplete || len(fresh.Observations[0].HTTP.Health.Checks) != 2 {
				t.Fatal("explicit new health observation failed")
			}
		})
	}
}

func TestConsulNativeHealthWatchACLFilteredSnapshotsUnknownWriteAndTerminalBudget(t *testing.T) {
	base, cfg, token := nativeClient(t)
	service, _ := nativeHealthFixture(t, base)
	if cfg.Auth != "none" {
		secret := os.Getenv("NYADB_CONSUL_READ_TOKEN")
		if secret == "" {
			t.Fatal("owned restricted token absent")
		}
		s, err := OpenNativeSession(context.Background(), 3203, cfg, secret)
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		l, err := s.ReserveKVHTTPWatch(1)
		if err != nil {
			t.Fatal(err)
		}
		defer l.Close()
		r := nativeHealthWatchRequest("service", service)
		got := settledHTTPWatch(t, startHTTPWatch(t, l, r))
		o := got.Observations[0]
		if !o.ProjectionComplete || o.HTTP.Health == nil || o.HTTP.Health.Complete || o.HTTP.Metadata.ACLFiltered == nil || !*o.HTTP.Metadata.ACLFiltered || len(o.HTTP.Health.Instances) != 0 {
			t.Fatal("ACL subset promoted to complete original inventory")
		}
	}
	relay, calls, processed := nativeFault(t, cfg, token, "lost")
	s, err := OpenNativeSession(context.Background(), 3204, relay, token)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	writer, err := s.ReserveKV(1)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	key, value := keyFor(t, "health-watch-unknown"), []byte{0, 255}
	unknown, err := writer.Execute(context.Background(), documentPlan(t, writer, putRequest(key, value)))
	wait(t, processed)
	if err == nil || !unknown.WriteOutcomeUnknown || !bytes.Equal(nativeRead(t, base, key).Value, value) {
		t.Fatal("original processed write fault absent")
	}
	written := nativeRead(t, base, key)
	before := calls.Load()
	recovery, err := s.RecoverKV(writer)
	if err != nil || recovery.Unknown == nil {
		t.Fatal(err)
	}
	defer recovery.Lease.Close()
	l, err := s.ReserveKVHTTPWatch(1)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	r := nativeHealthWatchRequest("checks", service)
	r.MaxBytes = 1
	r.MaxObservations = 10
	got := settledHTTPWatch(t, startHTTPWatch(t, l, r))
	if !got.TerminalOverflow || got.Terminal.Reason != "byte-limit" || len(got.Observations) != 1 || !got.Observations[0].ProjectionComplete || !bytes.Contains(got.Observations[0].HTTP.Body, []byte(service)) {
		t.Fatal("complete original terminal health receipt lost")
	}
	if !reflect.DeepEqual(*recovery.Lease.(*documentLease).recoveryOwned().Unknown, unknown) || nativeRead(t, base, key).ModifyIndex != written.ModifyIndex || calls.Load() <= before {
		t.Fatal("later health watch cleared or replayed original unknown write")
	}
	write, _ := DocumentRequest(putRequest(key, nil))
	if _, err := recovery.Lease.Prepare(write); !errors.Is(err, contract.ErrKVWriteUnknown) {
		t.Fatal("health watch enabled another write", err)
	}
	if index := got.Observations[0].HTTP.Metadata.Index; index == nil || strconv.FormatUint(*index, 10) == "" {
		t.Fatal("native uint64 health index lost")
	}
}
