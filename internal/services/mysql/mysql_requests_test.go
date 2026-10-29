package mysql

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func connectedRequestManager(t *testing.T) *MySQLSessionManager {
	t.Helper()
	m := NewMySQLSessionManager()
	m.opener = func(context.Context, connections.ActiveSettings, string) (*sql.DB, error) {
		return sql.OpenDB(mysqlSessionMockConnector{}), nil
	}
	if err := m.Connect(context.Background(), connections.ActiveSettings{ProfileID: 7}, ""); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close() })
	return m
}

func TestMySQLRequestContextRequiresConnectedSession(t *testing.T) {
	m := NewMySQLSessionManager()
	for _, closed := range []bool{false, true} {
		if closed {
			if err := m.Close(); err != nil {
				t.Fatal(err)
			}
		}
		session, ctx, cancel, err := m.requestContext(context.Background(), 7, 0)
		if !errors.Is(err, ErrMySQLNotConnected) || session != nil || ctx != nil || cancel != nil {
			t.Fatalf("closed=%v: absent session returned %v, %v, %v, %v", closed, session, ctx, cancel, err)
		}
	}
}

func TestMySQLRequestContextCancellation(t *testing.T) {
	for _, cause := range []string{"caller", "disconnect", "close", "cleanup"} {
		t.Run(cause, func(t *testing.T) {
			m := connectedRequestManager(t)
			caller, cancelCaller := context.WithCancel(context.Background())
			defer cancelCaller()
			_, request, cleanup, err := m.requestContext(caller, 7, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer cleanup()
			if _, bounded := request.Deadline(); bounded {
				t.Fatal("zero timeout imposed a request deadline")
			}
			_, other, cleanupOther, err := m.requestContext(context.Background(), 7, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer cleanupOther()
			switch cause {
			case "caller":
				cancelCaller()
			case "disconnect":
				if err := m.Disconnect(7); err != nil {
					t.Fatal(err)
				}
			case "close":
				if err := m.Close(); err != nil {
					t.Fatal(err)
				}
			case "cleanup":
				cleanup()
			}
			select {
			case <-request.Done():
			case <-time.After(time.Second):
				t.Fatal("request was not canceled")
			}
			if !errors.Is(request.Err(), context.Canceled) {
				t.Fatalf("cancellation returned %v", request.Err())
			}
			if cause == "caller" || cause == "cleanup" {
				if !m.IsConnected(7) || other.Err() != nil {
					t.Fatal("request cancellation affected its session or another request")
				}
			}
		})
	}
}

func TestMySQLRequestContextDeadlines(t *testing.T) {
	for _, timeout := range []time.Duration{0, time.Hour} {
		t.Run(timeout.String(), func(t *testing.T) {
			m := connectedRequestManager(t)
			caller, cancelCaller := context.WithTimeout(context.Background(), 20*time.Millisecond)
			defer cancelCaller()
			_, request, cleanup, err := m.requestContext(caller, 7, timeout)
			if err != nil {
				t.Fatal(err)
			}
			defer cleanup()
			want, _ := caller.Deadline()
			if deadline, ok := request.Deadline(); !ok || !deadline.Equal(want) {
				t.Fatalf("caller deadline changed: %v, %v", deadline, ok)
			}
			select {
			case <-request.Done():
			case <-time.After(time.Second):
				t.Fatal("caller deadline did not cancel request")
			}
			if !errors.Is(request.Err(), context.DeadlineExceeded) || !m.IsConnected(7) {
				t.Fatalf("request deadline affected session or error: %v", request.Err())
			}
		})
	}

	m := connectedRequestManager(t)
	_, request, cleanup, err := m.requestContext(context.Background(), 7, 20*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if _, ok := request.Deadline(); !ok {
		t.Fatal("operation timeout did not impose a deadline")
	}
	select {
	case <-request.Done():
	case <-time.After(time.Second):
		t.Fatal("operation timeout did not cancel request")
	}
	if !errors.Is(request.Err(), context.DeadlineExceeded) || !m.IsConnected(7) {
		t.Fatalf("operation deadline affected session or error: %v", request.Err())
	}
}
