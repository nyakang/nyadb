package dbruntime_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/dbruntime"
)

type keyPrefixDeleteHandle struct {
	mutationHandle
	calls    atomic.Int32
	captured contract.DataKeyPrefixDelete
	produce  func(contract.DataKeyPrefixDelete) (contract.DataMutationPlan, error)
}

func (h *keyPrefixDeleteHandle) DataRuntime() contract.DataRuntime {
	r := h.mutationHandle.DataRuntime()
	r.PreviewKeyPrefixDelete = func(_ context.Context, _ int64, _, _ string, request contract.DataKeyPrefixDelete) (contract.DataMutationPlan, error) {
		h.calls.Add(1)
		h.captured = request
		if h.produce != nil {
			return h.produce(request)
		}
		h.plan.snapshot.KeyPrefixDelete = &request
		return h.plan, nil
	}
	return r
}
func keyPrefixDeleteRuntime(t *testing.T) (*dbruntime.Manager, *[]*keyPrefixDeleteHandle) {
	t.Helper()
	m := dbruntime.New()
	t.Cleanup(func() { m.Close() })
	handles := []*keyPrefixDeleteHandle{}
	m.Register(connections.Consul, dbruntime.ProviderFunc(func(_ context.Context, p connections.Profile, _ string) (contract.Session, error) {
		h := &keyPrefixDeleteHandle{mutationHandle: mutationHandle{handle: handle{id: p.ID, caps: contract.Capabilities{EditableTables: true, KeyValues: true}}}}
		h.plan = &mutationPreview{snapshot: contract.DataMutationSnapshot{Object: h.Namespace("main").Object("table", "owned")}}
		handles = append(handles, h)
		return h, nil
	}))
	if _, err := m.ConnectSession(context.Background(), keyCopyProfile(), ""); err != nil {
		t.Fatal(err)
	}
	return m, &handles
}

func TestDataKeyPrefixDeleteRuntimeQualifiedObjectOriginalPrefixAndPlanOwnership(t *testing.T) {
	m, handles := keyPrefixDeleteRuntime(t)
	h := (*handles)[0]
	r := m.Data(1)
	request := contract.DataKeyPrefixDelete{Object: h.plan.snapshot.Object, Prefix: "scope//猫"}
	for _, fault := range []string{"unqualified", "profile", "catalog", "schema", "name"} {
		t.Run(fault, func(t *testing.T) {
			foreign := request
			switch fault {
			case "unqualified":
				foreign.Object.ProfileID = 0
			case "profile":
				foreign.Object.ProfileID = 2
			case "catalog":
				foreign.Object.Catalog = "foreign"
			case "schema":
				foreign.Object.Schema = "foreign"
			case "name":
				foreign.Object.Name = "foreign"
			}
			if p, err := r.PreviewKeyPrefixDelete(context.Background(), 1, "main", "owned", foreign); p != nil || !errors.Is(err, connections.ErrInvalid) || h.calls.Load() != 0 {
				t.Fatal("foreign prefix object reached original producer", err)
			}
		})
	}
	p, err := r.PreviewKeyPrefixDelete(context.Background(), 1, "main", "owned", request)
	if err != nil {
		t.Fatal(err)
	}
	request.Prefix = "foreign"
	request.Object.ProfileID = 7
	if h.captured.Prefix != "scope//猫" || h.captured.Object.ProfileID != 1 {
		t.Fatal("prefix followed caller edits")
	}
	if _, err := r.SubmitEdits(context.Background(), 1, "main", "owned", h.plan); !errors.Is(err, connections.ErrInvalid) {
		t.Fatal("raw prefix plan bypassed original binding", err)
	}
	if _, err := r.SubmitEdits(context.Background(), 1, "main", "foreign", p); !errors.Is(err, connections.ErrInvalid) {
		t.Fatal("prefix plan crossed original table", err)
	}
	if _, err := r.SubmitEdits(context.Background(), 1, "main", "owned", p); err != nil || h.saved.Load() != 1 {
		t.Fatal("original prefix plan not submitted", err)
	}
	for _, fault := range []string{"foreign-plan", "foreign-prefix", "missing-descriptor", "nil-plan", "cancel"} {
		t.Run(fault, func(t *testing.T) {
			request := contract.DataKeyPrefixDelete{Object: h.plan.snapshot.Object, Prefix: "scope/"}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			raw := &mutationPreview{snapshot: h.plan.snapshot}
			h.produce = func(request contract.DataKeyPrefixDelete) (contract.DataMutationPlan, error) {
				raw.snapshot.KeyPrefixDelete = &request
				switch fault {
				case "foreign-plan":
					raw.snapshot.Object.ProfileID = 2
				case "foreign-prefix":
					raw.snapshot.KeyPrefixDelete.Prefix = "foreign/"
				case "missing-descriptor":
					raw.snapshot.KeyPrefixDelete = nil
				case "nil-plan":
					return nil, nil
				case "cancel":
					cancel()
				}
				return raw, nil
			}
			if p, err := r.PreviewKeyPrefixDelete(ctx, 1, "main", "owned", request); p != nil || err == nil {
				t.Fatal("invalid prefix plan acquired original authority")
			}
			if fault != "nil-plan" && raw.closed.Load() != 1 {
				t.Fatal("invalid original prefix plan not closed")
			}
		})
	}
}

func TestDataKeyPrefixDeleteRuntimeLateOriginalGenerationClosesCapturedPlan(t *testing.T) {
	m, handles := keyPrefixDeleteRuntime(t)
	h := (*handles)[0]
	r := m.Data(1)
	entered, release := make(chan struct{}), make(chan struct{})
	h.produce = func(contract.DataKeyPrefixDelete) (contract.DataMutationPlan, error) {
		close(entered)
		<-release
		return h.plan, nil
	}
	type completion struct {
		plan contract.DataMutationPlan
		err  error
	}
	done := make(chan completion, 1)
	go func() {
		p, err := r.PreviewKeyPrefixDelete(context.Background(), 1, "main", "owned", contract.DataKeyPrefixDelete{Object: h.plan.snapshot.Object, Prefix: "scope/"})
		done <- completion{p, err}
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("original prefix preview not entered")
	}
	m.Disconnect(1)
	if _, err := m.ConnectSession(context.Background(), keyCopyProfile(), ""); err != nil {
		t.Fatal(err)
	}
	close(release)
	select {
	case c := <-done:
		if c.plan != nil || c.err == nil {
			t.Fatal("prefix followed replacement generation")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("late original prefix did not settle")
	}
	if h.plan.closed.Load() != 1 || (*handles)[1].calls.Load() != 0 {
		t.Fatal("late prefix closed replacement or retained captured plan")
	}
}
