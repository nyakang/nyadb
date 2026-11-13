package dbruntime_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/consul"
)

func consulRuntimeTTLRequest(t *testing.T, action, id string) consul.Request {
	t.Helper()
	output := "original processed output 中文"
	var r consul.Request
	var err error
	switch action {
	case "pass":
		r, err = consul.AgentCheckPassRequest(id, &output)
	case "warn":
		r, err = consul.AgentCheckWarnRequest(id, &output)
	case "fail":
		r, err = consul.AgentCheckFailRequest(id, &output)
	case "update":
		r, err = consul.AgentCheckUpdateRequest(id, []byte(` {"Status":"passing","Output":"original processed output 中文"} `))
	default:
		t.Fatal("unknown TTL action", action)
	}
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestConsulNativeRuntimeAgentTTLOriginalPublishedGenerationsAndRecovery(t *testing.T) {
	cfg, token := consulRuntimeConfig(t)
	relay, calls, writes, processed := consulRuntimeCheckFault(t, cfg, "observe")
	m := consulRuntimeManager(t, relay)
	profile := consulRuntimeProfile()
	if err := m.Connect(context.Background(), profile, token); err != nil {
		t.Fatal(err)
	}
	second, err := m.ConnectSession(context.Background(), profile, token)
	if err != nil || second.Generation() <= 1 {
		t.Fatal("public generation did not differ from the native context generation", err)
	}
	l := consulRuntimeReserve(t, m, 8311)
	id := "owned-" + t.Name()
	base, _, err := consul.Open(context.Background(), cfg, token)
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	if _, err := base.Do(context.Background(), consulRuntimeCheckRegister(t, id)); err != nil {
		t.Fatal(err)
	}
	r := consulRuntimeTTLRequest(t, "update", id)
	before := calls.Load()
	plan := consulRuntimePlan(t, l, r)
	if calls.Load() != before || !plan.Writes() || l.Scope().Generation != second.Generation() {
		t.Fatal("Agent preparation dispatched or lost its original publication")
	}
	original, err := l.Execute(context.Background(), plan)
	if err != nil || !original.Completed || original.HTTP == nil || !original.HTTP.Succeeded || len(original.HTTP.Body) != 0 || !bytes.Equal(original.HTTP.Request.Body, r.Body) || original.Native != nil || original.Header != (contract.KVHeader{}) {
		t.Fatal("runtime changed original empty Agent acknowledgement", err)
	}
	consulRuntimeAwait(t, processed)
	third, err := m.ConnectSession(context.Background(), profile, token)
	if err != nil || third.Generation() <= second.Generation() {
		t.Fatal("replacement publication did not advance", err)
	}
	if _, err := l.Execute(context.Background(), plan); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("original Agent plan acquired replacement authority", err)
	}
	fresh, err := m.KVRequests(profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	before = calls.Load()
	recovered, err := fresh.RecoverKV(l)
	if err != nil {
		t.Fatal(err)
	}
	defer recovered.Lease.Close()
	if calls.Load() != before || recovered.Lease.Scope().Generation != third.Generation() || recovered.Unknown != nil || !reflect.DeepEqual(recovered.Last, original) {
		t.Fatal("runtime recovery dispatched or changed original Agent receipt")
	}
	if _, err := recovered.Lease.Execute(context.Background(), plan); !errors.Is(err, contract.ErrKVPlanUsed) {
		t.Fatal("recovery granted foreign original Agent authority", err)
	}
	if err := recovered.Lease.Preconnect(context.Background()); err != nil {
		t.Fatal(err)
	}
	before = calls.Load()
	consulRuntimeDo(t, recovered.Lease, consulRuntimeTTLRequest(t, "warn", id))
	if calls.Load() != before+1 || writes.Load() != 2 {
		t.Fatal("explicit original TTL update did not dispatch exactly once")
	}
}

func TestConsulNativeRuntimeAgentTTLProcessedReplacementKeepsOriginalUnknown(t *testing.T) {
	cfg, token := consulRuntimeConfig(t)
	base, _, err := consul.Open(context.Background(), cfg, token)
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	for _, kind := range []string{"pass", "warn", "fail", "update"} {
		for _, mode := range []string{"http400", "http500", "http408", "lost", "partial", "cancel", "replace", "close"} {
			t.Run(kind+"/"+mode, func(t *testing.T) {
				id := "owned-" + strings.ReplaceAll(t.Name(), "/", "-")
				if _, err := base.Do(context.Background(), consulRuntimeCheckRegister(t, id)); err != nil {
					t.Fatal(err)
				}
				r := consulRuntimeTTLRequest(t, kind, id)
				relay, calls, writes, processed := consulRuntimeCheckFault(t, cfg, mode)
				m := consulRuntimeManager(t, relay)
				profile := consulRuntimeProfile()
				if err := m.Connect(context.Background(), profile, token); err != nil {
					t.Fatal(err)
				}
				second, err := m.ConnectSession(context.Background(), profile, token)
				if err != nil {
					t.Fatal(err)
				}
				l := consulRuntimeReserve(t, m, 8312)
				plan := consulRuntimePlan(t, l, r)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				done := make(chan struct{})
				var original contract.KVResult
				var executionErr error
				go func() { original, executionErr = l.Execute(ctx, plan); close(done) }()
				consulRuntimeAwait(t, processed)
				if mode == "cancel" {
					cancel()
				} else if mode == "replace" {
					if err := m.Connect(context.Background(), profile, token); err != nil {
						t.Fatal(err)
					}
				} else if mode == "close" {
					if err := m.Disconnect(profile.ID); err != nil {
						t.Fatal(err)
					}
				}
				consulRuntimeAwait(t, done)
				if executionErr == nil || !original.WriteOutcomeUnknown || original.Completed || original.HTTP == nil || original.HTTP.Acknowledged || !original.HTTP.Ended || writes.Load() != 1 {
					t.Fatal("runtime promoted or replayed processed Agent mutation", executionErr)
				}
				if err := m.Connect(context.Background(), profile, token); err != nil {
					t.Fatal(err)
				}
				fresh, err := m.KVRequests(profile.ID)
				if err != nil {
					t.Fatal(err)
				}
				before := calls.Load()
				recovered, err := fresh.RecoverKV(l)
				if err != nil {
					t.Fatal(err)
				}
				defer recovered.Lease.Close()
				if recovered.Lease.Scope().Generation <= second.Generation() || calls.Load() != before || recovered.Unknown == nil || !reflect.DeepEqual(*recovered.Unknown, original) {
					t.Fatal("runtime pure recovery changed original unknown/public generation")
				}
				if _, err := recovered.Lease.Execute(context.Background(), plan); !errors.Is(err, contract.ErrKVPlanUsed) || calls.Load() != before {
					t.Fatal("runtime recovery granted original plan authority", err)
				}
				for _, request := range []consul.Request{r, consulRuntimePut(t, "later/"+id, nil)} {
					doc, err := consul.DocumentRequest(request)
					if err != nil {
						t.Fatal(err)
					}
					if _, err := recovered.Lease.Prepare(doc); !errors.Is(err, contract.ErrKVWriteUnknown) || calls.Load() != before {
						t.Fatal("runtime original unknown barrier removed", err)
					}
				}
				consulRuntimeDo(t, recovered.Lease, consul.AgentSelfRequest())
				fresh2, err := fresh.RecoverKV(recovered.Lease)
				if err != nil {
					t.Fatal(err)
				}
				defer fresh2.Lease.Close()
				if fresh2.Unknown == nil || !reflect.DeepEqual(*fresh2.Unknown, original) || writes.Load() != 1 {
					t.Fatal("later Agent read replaced original processed-write evidence")
				}
				read, err := consul.AgentChecksRequest(nil)
				if err != nil {
					t.Fatal(err)
				}
				values, err := base.Do(context.Background(), read)
				var checks map[string]json.RawMessage
				if err != nil || json.Unmarshal(values.Body, &checks) != nil {
					t.Fatal("actual original check map unavailable", err)
				}
				var check struct{ Status, Output string }
				if json.Unmarshal(checks[id], &check) != nil || check.Status != map[string]string{"pass": "passing", "warn": "warning", "fail": "critical", "update": "passing"}[kind] || check.Output != "original processed output 中文" {
					t.Fatal("runtime fault did not follow actual original TTL mutation")
				}
			})
		}
	}
}
