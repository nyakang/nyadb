package consul

import (
	"bytes"
	"context"
	"errors"
	"github.com/nyakang/nyadb/internal/db/contract"
	"io"
	"net/http"
	"reflect"
	"testing"
)

func TestHTTPArchiveRestoreValidatesOriginalBeforeDispatchAndKeepsPrivateReceipt(t *testing.T) {
	original := snapshotTestOriginal(t)
	s, calls := documentSession(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/snapshot" || r.Method != "PUT" {
			t.Error("restore authority changed")
		}
		b, _ := io.ReadAll(r.Body)
		if !bytes.Equal(b, original) {
			t.Error("archive changed")
		}
		if r.Header.Get("Content-Type") != "application/octet-stream" {
			t.Error("archive MIME changed")
		}
	})
	s.cfg.RestoreWrite = true
	s.opener = func(ctx context.Context) (*Client, OpenEvidence, error) { return Open(ctx, s.cfg, "") }
	l, _ := s.ReserveKV(1)
	defer l.Close()
	other, _ := s.ReserveKV(2)
	defer other.Close()
	facet := l.(contract.KVHTTPArchiveRestoring)
	before := calls.Load()
	if _, err := facet.PrepareHTTPArchiveRestore(contract.KVHTTPArchiveRestoreRequest{Archive: []byte("bad"), ExpandedBytes: 2 << 20}); err == nil || calls.Load() != before {
		t.Fatal("invalid archive dispatched")
	}
	in := contract.KVHTTPArchiveRestoreRequest{Archive: bytes.Clone(original), ExpandedBytes: 2 << 20}
	plan, err := facet.PrepareHTTPArchiveRestore(in)
	if err != nil {
		t.Fatal(err)
	}
	in.Archive[0] = '!'
	inspection := plan.Request()
	inspection.Archive[0] = '!'
	if calls.Load() != before || !bytes.Equal(plan.Request().Archive, original) {
		t.Fatal("preparation mutated source or dispatched")
	}
	if _, err := other.(contract.KVHTTPArchiveRestoring).ExecuteHTTPArchiveRestore(context.Background(), plan); !errors.Is(err, contract.ErrKVPlanUsed) {
		t.Fatal("foreign plan accepted")
	}
	r, err := facet.ExecuteHTTPArchiveRestore(context.Background(), plan)
	if err != nil || !r.Completed || !r.HTTP.Succeeded || r.WriteOutcomeUnknown {
		t.Fatal("native complete empty 200 lost", err)
	}
	if other.CheckKVLease() == nil || s.Alive() || l.LastResult().HTTP != nil {
		t.Fatal("restore failed to quiesce or exposed secrets generically")
	}
	if !reflect.DeepEqual(r, facet.LastHTTPArchiveRestore()) {
		t.Fatal("private restore receipt incomplete")
	}
	if _, err = facet.ExecuteHTTPArchiveRestore(context.Background(), plan); err == nil {
		t.Fatal("restore replayed")
	}
	r.Request.Archive[0] = '!'
	if !bytes.Equal(facet.LastHTTPArchiveRestore().Request.Archive, original) {
		t.Fatal("private archive aliases inspection")
	}
}
func TestHTTPArchiveRestorePartialAndMalformedNativeAcknowledgementsStayUnknown(t *testing.T) {
	for _, mode := range []string{"body", "truncated", "lost"} {
		t.Run(mode, func(t *testing.T) {
			s, _ := documentSession(t, func(w http.ResponseWriter, r *http.Request) {
				switch mode {
				case "body":
					w.Write([]byte("true"))
				case "truncated":
					w.Header().Set("Content-Length", "8")
					w.Write([]byte("x"))
				case "lost":
					h, ok := w.(http.Hijacker)
					if !ok {
						t.Fatal("hijack unavailable")
					}
					c, _, _ := h.Hijack()
					c.Close()
				}
			})
			s.cfg.RestoreWrite = true
			s.opener = func(ctx context.Context) (*Client, OpenEvidence, error) { return Open(ctx, s.cfg, "") }
			l, _ := s.ReserveKV(1)
			defer l.Close()
			facet := l.(contract.KVHTTPArchiveRestoring)
			p, err := facet.PrepareHTTPArchiveRestore(contract.KVHTTPArchiveRestoreRequest{Archive: snapshotTestOriginal(t), ExpandedBytes: 2 << 20})
			if err != nil {
				t.Fatal(err)
			}
			r, err := facet.ExecuteHTTPArchiveRestore(context.Background(), p)
			if err == nil || !r.Attempted || r.Completed || !r.WriteOutcomeUnknown || !l.(*documentLease).writeUnknown.Load() {
				t.Fatal("restore uncertainty promoted", err)
			}
			if !reflect.DeepEqual(r, facet.LastHTTPArchiveRestore()) {
				t.Fatal("unknown original erased")
			}
		})
	}
}
