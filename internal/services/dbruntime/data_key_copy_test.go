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

type keyCopyHandle struct {
	mutationHandle
	captured    contract.DataKeyCopy
	copies      atomic.Int32
	copyPreview func(contract.DataKeyCopy) (contract.DataMutationPlan, error)
}

func (h *keyCopyHandle) DataRuntime() contract.DataRuntime {
	r := h.mutationHandle.DataRuntime()
	r.PreviewKeyCopy = func(_ context.Context, _ int64, _, _ string, request contract.DataKeyCopy) (contract.DataMutationPlan, error) {
		h.copies.Add(1)
		h.captured = request
		if h.copyPreview != nil {
			return h.copyPreview(request)
		}
		return h.plan, nil
	}
	return r
}

func keyCopyProfile() connections.Profile {
	return connections.Profile{ID: 1, Name: "Conditional copy fixture", Kind: connections.Consul, Host: "127.0.0.1", Port: 8500, TLSMode: "disable", ConsulOptions: connections.DefaultConsulOptions()}
}

func keyCopyRuntime(t *testing.T) (*dbruntime.Manager, *[]*keyCopyHandle) {
	t.Helper()
	m := dbruntime.New()
	t.Cleanup(func() { m.Close() })
	handles := []*keyCopyHandle{}
	m.Register(connections.Consul, dbruntime.ProviderFunc(func(_ context.Context, p connections.Profile, _ string) (contract.Session, error) {
		h := &keyCopyHandle{mutationHandle: mutationHandle{handle: handle{id: p.ID, caps: contract.Capabilities{EditableTables: true, KeyValues: true}}}}
		h.plan = &mutationPreview{snapshot: contract.DataMutationSnapshot{Object: h.Namespace("main").Object("table", "owned")}}
		handles = append(handles, h)
		return h, nil
	}))
	if _, err := m.ConnectSession(context.Background(), keyCopyProfile(), ""); err != nil {
		t.Fatal(err)
	}
	return m, &handles
}

func keyCopyRequest(h *keyCopyHandle) contract.DataKeyCopy {
	fingerprint := "original-fingerprint"
	return contract.DataKeyCopy{Source: &contract.RowSnapshot{
		Ref:     contract.RowRef{Object: h.plan.snapshot.Object, Values: [][]byte{{0, 255}}, TypedValues: []contract.Cell{{Kind: contract.Binary, Bytes: []byte{0, 255}}}},
		Columns: []string{"key", "value"}, Original: []contract.Cell{{Kind: contract.Binary, Bytes: []byte{0, 255}}}, Fingerprints: []*string{&fingerprint}, Metadata: "original-metadata",
	}, Destination: "owned/target"}
}

func TestDataKeyCopyRuntimeCapturesBothPayloadsAndRejectsForeignSourceAndPlanBeforeDispatch(t *testing.T) {
	m, handles := keyCopyRuntime(t)
	r, h := m.Data(1), (*handles)[0]
	ctx := context.Background()
	for _, variation := range []string{"missing", "unqualified", "profile", "catalog", "schema", "table"} {
		t.Run(variation, func(t *testing.T) {
			request := keyCopyRequest(h)
			switch variation {
			case "missing":
				request.Source = nil
			case "unqualified":
				request.Source.Ref.Object.ProfileID = 0
			case "profile":
				request.Source.Ref.Object.ProfileID = 2
			case "catalog":
				request.Source.Ref.Object.Catalog = "foreign"
			case "schema":
				request.Source.Ref.Object.Schema = "foreign"
			case "table":
				request.Source.Ref.Object.Name = "foreign"
			}
			if p, err := r.PreviewKeyCopy(ctx, 1, "main", "owned", request); p != nil || !errors.Is(err, connections.ErrInvalid) || h.copies.Load() != 0 {
				t.Fatal("foreign source reached original producer", err)
			}
		})
	}
	for _, mode := range []string{"values", "fingerprints"} {
		request := keyCopyRequest(h)
		request.Source.Mode = mode
		if mode == "values" {
			request.Source.Fingerprints = nil
		} else {
			request.Source.Original = nil
		}
		plan, err := r.PreviewKeyCopy(ctx, 1, "main", "owned", request)
		if err != nil {
			t.Fatal(err)
		}
		request.Source.Ref.Values[0][0] = 99
		request.Source.Ref.TypedValues[0].Bytes[0] = 99
		request.Source.Columns[0] = "foreign"
		request.Source.Metadata = "foreign"
		request.Destination = "foreign"
		if mode == "values" {
			request.Source.Original[0].Bytes[0] = 99
		} else {
			*request.Source.Fingerprints[0] = "foreign"
		}
		got := h.captured
		if got.Source.Ref.Values[0][0] != 0 || got.Source.Ref.TypedValues[0].Bytes[0] != 0 || got.Source.Columns[0] != "key" || got.Source.Metadata != "original-metadata" || got.Destination != "owned/target" || got.Source.Mode != mode {
			t.Fatal("copy adopted mutable caller fields")
		}
		if mode == "values" && got.Source.Original[0].Bytes[0] != 0 || mode == "fingerprints" && *got.Source.Fingerprints[0] != "original-fingerprint" {
			t.Fatal("copy adopted mutable validation payload")
		}
		if _, err := r.SubmitEdits(ctx, 1, "main", "owned", h.plan); !errors.Is(err, connections.ErrInvalid) {
			t.Fatal("raw copy plan bypassed original binding", err)
		}
		if _, err := r.SubmitEdits(ctx, 1, "main", "foreign", plan); !errors.Is(err, connections.ErrInvalid) {
			t.Fatal("copy plan crossed table", err)
		}
		if _, err := r.SubmitEdits(ctx, 1, "main", "owned", plan); err != nil {
			t.Fatal(err)
		}
		plan.Close()
	}
	if h.saved.Load() != 2 {
		t.Fatal("original copy plan was not unwrapped")
	}
}

func TestDataKeyCopyRuntimeRejectsInvalidProviderPlansCancellationAndMissingCapabilities(t *testing.T) {
	for _, variation := range []string{"profile", "catalog", "table", "kind", "nil", "error", "mutate-request", "cancel", "snapshot-replacement", "readonly", "non-kv"} {
		t.Run(variation, func(t *testing.T) {
			m, handles := keyCopyRuntime(t)
			h := (*handles)[0]
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			original := keyCopyRequest(h)
			plan := h.plan
			h.copyPreview = func(request contract.DataKeyCopy) (contract.DataMutationPlan, error) {
				switch variation {
				case "profile":
					plan.snapshot.Object.ProfileID = 2
				case "catalog":
					plan.snapshot.Object.Catalog = "foreign"
				case "table":
					plan.snapshot.Object.Name = "foreign"
				case "kind":
					plan.snapshot.Object.Kind = "foreign"
				case "nil":
					return nil, nil
				case "error":
					return plan, connections.ErrInvalid
				case "mutate-request":
					request.Source.Ref.Object.Name = "foreign"
					plan.snapshot.Object = request.Source.Ref.Object
				case "cancel":
					cancel()
				case "snapshot-replacement":
					return &replacingCopyPlan{mutationPreview: plan, replace: func() {
						if _, err := m.ConnectSession(context.Background(), keyCopyProfile(), ""); err != nil {
							t.Fatal(err)
						}
					}}, nil
				}
				return plan, nil
			}
			if variation == "readonly" {
				h.caps.EditableTables = false
			}
			if variation == "non-kv" {
				h.caps.KeyValues = false
			}
			r := m.Data(1)
			p, err := r.PreviewKeyCopy(ctx, 1, "main", "owned", original)
			if p != nil || err == nil {
				t.Fatal("invalid original copy was published")
			}
			if variation == "readonly" || variation == "non-kv" {
				if !errors.Is(err, contract.ErrUnsupported) || h.copies.Load() != 0 || plan.closed.Load() != 0 {
					t.Fatal("unsupported copy reached producer", err)
				}
			} else if variation == "nil" {
				if !errors.Is(err, contract.ErrUnsupported) {
					t.Fatal(err)
				}
			} else if plan.closed.Load() != 1 {
				t.Fatal("invalid copy plan was not closed", variation)
			}
		})
	}
}

type replacingCopyPlan struct {
	*mutationPreview
	replace func()
}

func (p *replacingCopyPlan) Snapshot() contract.DataMutationSnapshot {
	p.replace()
	return p.mutationPreview.Snapshot()
}

func TestDataKeyCopyRuntimeLatePreviewClosesOnlyOriginalPlanAndCannotFollowReplacement(t *testing.T) {
	m, handles := keyCopyRuntime(t)
	r, h := m.Data(1), (*handles)[0]
	request := keyCopyRequest(h)
	entered, release := make(chan struct{}), make(chan struct{})
	h.copyPreview = func(contract.DataKeyCopy) (contract.DataMutationPlan, error) {
		close(entered)
		<-release
		return h.plan, nil
	}
	done := make(chan error, 1)
	go func() {
		p, err := r.PreviewKeyCopy(context.Background(), 1, "main", "owned", request)
		if p != nil {
			p.Close()
		}
		done <- err
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("original copy did not enter")
	}
	if _, err := m.ConnectSession(context.Background(), keyCopyProfile(), ""); err != nil {
		t.Fatal(err)
	}
	replacement := (*handles)[1]
	close(release)
	select {
	case err := <-done:
		if !errors.Is(err, contract.ErrRevoked) || h.plan.closed.Load() != 1 || replacement.plan.closed.Load() != 0 {
			t.Fatal("late original plan damaged replacement", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("original producer did not settle")
	}
	if _, err := r.PreviewKeyCopy(context.Background(), 1, "main", "owned", request); !errors.Is(err, contract.ErrRevoked) || h.copies.Load() != 1 {
		t.Fatal("old copy runtime followed replacement", err)
	}
	if !m.IsConnected(1) || replacement.copies.Load() != 0 {
		t.Fatal("late original copy reached replacement")
	}
}
