package dbruntime_test

import (
	"bytes"
	"context"
	"errors"
	"net/url"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"encoding/json"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/dbruntime"
)

func consulMutationRuntime(t *testing.T, p connections.Profile, secret string) (*dbruntime.Manager, uint64, contract.KVRequestLease) {
	t.Helper()
	m := dbruntime.New()
	dbruntime.RegisterBuiltins(m, nil)
	t.Cleanup(func() { m.Close() })
	h, err := m.ConnectSession(context.Background(), p, secret)
	if err != nil {
		t.Fatal(err)
	}
	kv, err := m.KVRequests(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	w, err := kv.ReserveKV(99)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { w.Close() })
	return m, h.Generation(), w
}

func consulMutationRuntimeEdit(t *testing.T, r contract.DataRuntime, id int64) contract.DataEdit {
	t.Helper()
	p, err := r.AdvancedLoader(context.Background(), id, "kv", "keys", contract.DataOptions{Limit: 10})
	if err != nil || p == nil || len(p.RowRefs) != 1 {
		t.Fatal("original page missing", err)
	}
	s, err := r.RowReader(context.Background(), id, "kv", "keys", *p.RowRefs[0])
	if err != nil {
		t.Fatal(err)
	}
	return contract.DataEdit{Kind: "update", Original: s.Snapshot, Values: map[string]contract.DataValue{"value": {Kind: contract.Binary, Bytes: []byte{0, 255, 7}}, "flags": {Kind: contract.Integer, Text: "9007199254740993"}}}
}

func TestConsulNativePublicDataMutationQualifiedOriginalPlansAndGenerationReplacement(t *testing.T) {
	p, secret := consulPublicProfile(t)
	p.ConsulOptions.Prefix = "native/public-runtime-mutation/" + t.Name() + "/"
	m, generation, writer := consulMutationRuntime(t, p, secret)
	key := p.ConsulOptions.Prefix + "key"
	consulRuntimeDo(t, writer, consulRuntimePut(t, key, []byte{255, 0}))
	l, peer := consulDataReserve(t, m, p.ID), consulDataReserve(t, m, p.ID)
	r := l.Runtime()
	edit := consulMutationRuntimeEdit(t, r, p.ID)
	ctx := context.Background()
	var foreign contract.DataEdit
	raw, _ := json.Marshal(edit)
	if json.Unmarshal(raw, &foreign) != nil {
		t.Fatal("copy original edit")
	}
	foreign.Original.Ref.Object.ProfileID++
	if _, err := r.PreviewEdits(ctx, p.ID, "kv", "keys", []contract.DataEdit{foreign}); !errors.Is(err, connections.ErrInvalid) {
		t.Fatal("foreign original accepted", err)
	}
	plan, err := r.PreviewEdits(ctx, p.ID, "kv", "keys", []contract.DataEdit{edit})
	if err != nil {
		t.Fatal(err)
	}
	defer plan.Close()
	edit.Values["value"].Bytes[0] = 42
	copy := plan.Snapshot()
	copy.Edits[0].Values["value"].Bytes[0] = 43
	if plan.Snapshot().Edits[0].Values["value"].Bytes[0] != 0 {
		t.Fatal("original public plan shares caller bytes")
	}
	if _, err := peer.Runtime().SubmitEdits(ctx, p.ID, "kv", "keys", plan); !errors.Is(err, contract.ErrKVPlanUsed) {
		t.Fatal("plan crossed document", err)
	}
	if _, err := r.SubmitEdits(ctx, p.ID, "other", "keys", plan); !errors.Is(err, connections.ErrInvalid) {
		t.Fatal("plan crossed namespace", err)
	}
	out, err := r.SubmitEdits(ctx, p.ID, "kv", "keys", plan)
	if err != nil || out == nil || !out.CommitConfirmed || out.CommitUncertain || out.NativeJSON == "" {
		t.Fatal("original public commit not established", err)
	}
	if _, err := r.SubmitEdits(ctx, p.ID, "kv", "keys", plan); err == nil {
		t.Fatal("original public plan replayed")
	}
	observed := consulRuntimeDo(t, writer, consulRuntimeRead(t, key))
	if !bytes.Equal(observed.HTTP.Entries[0].Value, []byte{0, 255, 7}) || observed.HTTP.Entries[0].Flags != 9007199254740993 {
		t.Fatal("original bytes/flags changed")
	}
	edit = consulMutationRuntimeEdit(t, r, p.ID)
	oldPlan, err := r.PreviewEdits(ctx, p.ID, "kv", "keys", []contract.DataEdit{edit})
	if err != nil {
		t.Fatal(err)
	}
	defer oldPlan.Close()
	fresh, err := m.ConnectSession(ctx, p, secret)
	if err != nil || fresh.Generation() <= generation {
		t.Fatal("replacement generation failed", err)
	}
	if _, err := r.SubmitEdits(ctx, p.ID, "kv", "keys", oldPlan); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("old runtime selected replacement", err)
	}
	newLease := consulDataReserve(t, m, p.ID)
	if _, err := newLease.Runtime().SubmitEdits(ctx, p.ID, "kv", "keys", oldPlan); !errors.Is(err, connections.ErrInvalid) {
		t.Fatal("old plan crossed generation", err)
	}
	l.Close()
	peer.Close()
	if !m.IsConnected(p.ID) {
		t.Fatal("old close retired replacement")
	}
	if n, err := newLease.Runtime().Count(ctx, p.ID, "kv", "keys"); err != nil || n != 1 {
		t.Fatal("replacement lost original namespace", err)
	}
}

func TestConsulNativePublicDataMutationProcessedRepliesPreserveOriginalUnknownAndNeverReplay(t *testing.T) {
	base, secret := consulPublicProfile(t)
	cfg, _ := consulRuntimeConfig(t)
	for _, mode := range []string{"lost", "partial", "cancel", "replace", "close"} {
		t.Run(mode, func(t *testing.T) {
			p := base
			p.ConsulOptions.Prefix = "native/public-runtime-mutation-fault/" + t.Name() + "/"
			_, _, observer := consulMutationRuntime(t, p, secret)
			key := p.ConsulOptions.Prefix + "key"
			consulRuntimeDo(t, observer, consulRuntimePut(t, key, []byte("original")))
			armed := &atomic.Bool{}
			proxy, calls, processed := consulRuntimeFault(t, cfg, "grid-"+mode, armed)
			u, _ := url.Parse(proxy.Endpoint)
			port, _ := strconv.Atoi(u.Port())
			p.Host, p.Port, p.TLSMode = u.Hostname(), uint16(port), "disable"
			p.ConsulOptions.CAFile, p.ConsulOptions.ClientCertFile, p.ConsulOptions.ClientKeyFile = "", "", ""
			m, generation, _ := consulMutationRuntime(t, p, secret)
			l := consulDataReserve(t, m, p.ID)
			r := l.Runtime()
			edit := consulMutationRuntimeEdit(t, r, p.ID)
			plan, err := r.PreviewEdits(context.Background(), p.ID, "kv", "keys", []contract.DataEdit{edit})
			if err != nil {
				t.Fatal(err)
			}
			defer plan.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			type completion struct {
				result *contract.DataEditResult
				err    error
			}
			done := make(chan completion, 1)
			armed.Store(true)
			go func() { out, err := r.SubmitEdits(ctx, p.ID, "kv", "keys", plan); done <- completion{out, err} }()
			consulRuntimeAwait(t, processed)
			switch mode {
			case "cancel":
				cancel()
			case "close":
				l.Close()
			case "replace":
				fresh, err := m.ConnectSession(context.Background(), p, secret)
				if err != nil || fresh.Generation() <= generation {
					t.Fatal("replacement failed", err)
				}
			}
			select {
			case got := <-done:
				if got.err == nil || got.result == nil || got.result.CommitConfirmed || !got.result.CommitUncertain || got.result.NativeJSON == "" {
					t.Fatal("processed original write lost unknown evidence", got.err)
				}
			case <-time.After(20 * time.Second):
				t.Fatal("original executor did not acknowledge termination")
			}
			if _, err := r.SubmitEdits(context.Background(), p.ID, "kv", "keys", plan); err == nil || calls.Load() != 1 {
				t.Fatal("original public write replayed", err, calls.Load())
			}
			if mode != "replace" && mode != "close" {
				if n, err := r.Count(context.Background(), p.ID, "kv", "keys"); err != nil || n != 1 {
					t.Fatal("unknown write disabled independent count", err)
				}
				if _, err := r.PreviewEdits(context.Background(), p.ID, "kv", "keys", []contract.DataEdit{edit}); err == nil || calls.Load() != 1 {
					t.Fatal("unknown original write authorized a new plan")
				}
			}
			actual := consulRuntimeDo(t, observer, consulRuntimeRead(t, key))
			if len(actual.HTTP.Entries) != 1 || !bytes.Equal(actual.HTTP.Entries[0].Value, []byte{0, 255, 7}) {
				t.Fatal("relay did not observe an actual original write")
			}
			l.Close()
			if mode == "replace" && !m.IsConnected(p.ID) {
				t.Fatal("late original close retired replacement")
			}
		})
	}
}
