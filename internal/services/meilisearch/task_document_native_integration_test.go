package meilisearch

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func TestMeilisearchNativeTaskDocumentLeasesCursorsOriginalRecordsAndSessionClose(t *testing.T) {
	cfg, secret := nativeFixture(t)
	s := testSession(t, cfg, secret)
	c := nativeOpen(t)
	uid := nativeIndex(t, c)
	for i := 0; i < 24; i++ {
		nativeWait(t, c, nativeSubmit(t, c, Request{Method: "PATCH", Path: "/indexes/" + uid + "/settings", Body: []byte(`{"stopWords":[]}`)}))
	}
	target := s.Namespace(uid).Object("tasks", "tasks")
	if !s.SupportsTaskDocument(target) || !s.SupportsTaskDocument(s.Namespace("").Object("tasks", "tasks")) {
		t.Fatal("native task scope unavailable")
	}
	lease, err := s.ReserveTaskDocument(target)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	first, err := lease.List(context.Background(), contract.TaskQuery{Limit: 10})
	if err != nil || first.Total != 25 || len(first.Results) != 10 || first.Next == "" {
		t.Fatal("native original task page", err)
	}
	second, err := lease.List(context.Background(), contract.TaskQuery{Limit: 10, From: first.Next})
	if err != nil || len(second.Results) != 10 || second.Next == "" || second.Results[0].UID == first.Results[0].UID {
		t.Fatal("native signed next task page", err)
	}
	third, err := lease.List(context.Background(), contract.TaskQuery{Limit: 10, From: second.Next})
	if err != nil || len(third.Results) != 5 || third.Next != "" {
		t.Fatal("native original last task page", err)
	}
	back, err := lease.List(context.Background(), contract.TaskQuery{Limit: 10, From: first.Next})
	if err != nil || back.Results[0].UID != second.Results[0].UID {
		t.Fatal("native task cursor back changed", err)
	}
	original := first.Results[0]
	for _, query := range []contract.TaskQuery{{Limit: 20, From: first.Next}, {Limit: 10, From: first.Next, Reverse: true}, {Limit: 10, From: first.Next, Statuses: []string{"failed"}}} {
		if _, err := lease.List(context.Background(), query); !errors.Is(err, ErrInvalid) {
			t.Fatal("cursor rebound to another query", err)
		}
	}
	other, err := s.ReserveTaskDocument(target)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if _, err := other.Read(context.Background(), original); !errors.Is(err, ErrInvalid) {
		t.Fatal("record rebound to another physical lease", err)
	}
	if _, err := other.List(context.Background(), contract.TaskQuery{Limit: 10, From: first.Next}); !errors.Is(err, ErrInvalid) {
		t.Fatal("cursor rebound to another physical lease", err)
	}
	observed, err := lease.Read(context.Background(), original)
	if err != nil || observed.UID != original.UID || observed.Raw != original.Raw || observed.Status != "succeeded" || observed.Details == "" || observed.FinishedAt == "" || observed.Duration == "" {
		t.Fatal("complete original native task lost", err)
	}
	if observed, err = lease.Observe(context.Background(), observed, time.Second); err != nil || observed.Status != "succeeded" {
		t.Fatal("explicit native observer failed", err)
	}
	global, err := s.ReserveTaskDocument(s.Namespace("").Object("tasks", "tasks"))
	if err != nil {
		t.Fatal(err)
	}
	defer global.Close()
	page, err := global.List(context.Background(), contract.TaskQuery{Limit: 10, Indexes: []string{uid}})
	if err != nil || page.Total != 25 {
		t.Fatal("global task page native index filter changed", err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = lease.List(context.Background(), contract.TaskQuery{Limit: 10}); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("old task lease followed session close", err)
	}
	t.Log("native task document owns original physical executor, opaque query-bound cursors, immutable complete task evidence and original-session close; 25 tasks over three pages and back")
}

func TestMeilisearchNativeTaskDocumentReadInterruptionsAndNoReplay(t *testing.T) {
	upstream := nativeOpen(t)
	uid := nativeIndex(t, upstream)
	for _, fault := range []string{"lost", "canceled", "closed"} {
		entered := make(chan struct{})
		var calls atomic.Int32
		relay := nativeRelay(t, upstream, func(w http.ResponseWriter, r *http.Request, response Response) bool {
			if !strings.HasSuffix(r.URL.Path, "/tasks") {
				return false
			}
			calls.Add(1)
			close(entered)
			if fault != "lost" {
				<-r.Context().Done()
				return true
			}
			conn, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				_ = conn.Close()
			}
			return true
		})
		cfg, secret := nativeFixture(t)
		cfg.Endpoint = relay.config.Endpoint
		cfg.CAFile = relay.config.CAFile
		cfg.ServerName = relay.config.ServerName
		cfg.ClientCertFile, cfg.ClientKeyFile = "", ""
		s := testSession(t, cfg, secret)
		lease, err := s.ReserveTaskDocument(s.Namespace(uid).Object("tasks", "tasks"))
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { _, err := lease.List(ctx, contract.TaskQuery{Limit: 1}); done <- err }()
		select {
		case <-entered:
		case <-time.After(10 * time.Second):
			cancel()
			t.Fatal("task executor not entered")
		}
		if fault == "canceled" {
			cancel()
		}
		if fault == "closed" {
			_ = s.Close()
		}
		select {
		case err := <-done:
			if !errors.Is(err, contract.ErrTaskLeaseLost) {
				t.Fatal("original task read not retired", err)
			}
		case <-time.After(10 * time.Second):
			cancel()
			t.Fatal("task executor not acknowledged")
		}
		cancel()
		if _, err := lease.List(context.Background(), contract.TaskQuery{Limit: 1}); !errors.Is(err, contract.ErrTaskLeaseLost) || calls.Load() != 1 {
			t.Fatal("lost task read replayed", err)
		}
		_ = lease.Close()
		_ = s.Close()
		// A fresh original-profile reservation performs no automatic replay.
		cfg, secret = nativeFixture(t)
		fresh := testSession(t, cfg, secret)
		restored, err := fresh.ReserveTaskDocument(fresh.Namespace(uid).Object("tasks", "tasks"))
		if err != nil {
			t.Fatal(err)
		}
		if page, err := restored.List(context.Background(), contract.TaskQuery{Limit: 1}); err != nil || len(page.Results) != 1 {
			t.Fatal("explicit original task recovery failed", err)
		}
		_ = restored.Close()
		_ = fresh.Close()
	}
	t.Log("native task read lost/cancel/session-close waits original executor, retires old lease, submits once; recovery reserves original profile and requires explicit read")
}
