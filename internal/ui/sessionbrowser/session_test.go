package sessionbrowser

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/services/connections"
	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
)

func testProfile(id int64) connections.Profile {
	return connections.Profile{ID: id, Name: "Test", Kind: connections.MySQL, Host: "localhost", Port: 3306, MySQLAdvanced: connections.DefaultMySQLAdvanced()}
}
func awaitJob(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("background job did not finish")
	}
}
func connectedState(t *testing.T) (*State, chan struct{}, func()) {
	t.Helper()
	s := New(Dependencies{})
	t.Cleanup(s.Close)
	done := make(chan struct{}, 16)
	notify := func() { done <- struct{}{} }
	s.Opener = func(context.Context, connections.ActiveSettings, string) error { return nil }
	s.DatabaseLister = func(context.Context, int64) ([]string, error) { return []string{"app", "other"}, nil }
	s.Connect(testProfile(1), notify)
	awaitJob(t, done)
	s.Consume()
	if s.Current().Status != "已连接" {
		t.Fatal(s.Current().Error)
	}
	return s, done, notify
}

func TestSessionBrowserParkedHandshakeKeepsActiveWorkspace(t *testing.T) {
	s := New(Dependencies{})
	defer s.Close()
	done := make(chan struct{}, 4)
	notify := func() { done <- struct{}{} }
	gate := make(chan struct{})
	var once sync.Once
	finish := func() { once.Do(func() { close(gate) }) }
	defer finish()
	s.Opener = func(ctx context.Context, settings connections.ActiveSettings, _ string) error {
		if settings.ProfileID == 1 {
			select {
			case <-gate:
			case <-ctx.Done():
			}
		}
		return nil
	}
	s.DatabaseLister = func(_ context.Context, id int64) ([]string, error) {
		if id == 1 {
			return []string{"only_a"}, nil
		}
		return []string{"shared"}, nil
	}
	s.Current().Password = "temporary"
	s.Connect(testProfile(1), notify)
	first := s.Current()
	if first.Password != "" {
		t.Fatal("connect retained password draft")
	}
	s.Activate(2)
	s.Connect(testProfile(2), notify)
	second := s.Current()
	awaitJob(t, done)
	s.Consume()
	if first.Status != "连接中" || second.Status != "已连接" {
		t.Fatal("handshake was lost or assigned to another profile")
	}
	// Complete A without choosing it; only its parked slot should change.
	finish()
	awaitJob(t, done)
	s.Consume()
	if s.Current() != second || s.StatusFor(1) != "已连接" || s.DatabasesFor(1)[0] != "only_a" {
		t.Fatal("parked completion changed the active workspace")
	}
	s.Activate(1)
	if s.Current() != first || s.Parked(2) != second {
		t.Fatal("activation copied or replaced workspace identity")
	}
	s.Disconnect(2)
	if s.StatusFor(1) != "已连接" || s.Parked(2) != nil {
		t.Fatal("disconnecting parked profile damaged active profile")
	}
}
func TestSessionBrowserReconnectNeverReusesGeneration(t *testing.T) {
	s, done, notify := connectedState(t)
	before := s.Current().Generation
	old := make(chan sessionResult, 1)
	s.Current().sessionResults = old
	s.Disconnect(1)
	s.Connect(testProfile(1), notify)
	awaitJob(t, done)
	s.Consume()
	after := s.Current().Generation
	if after <= before {
		t.Fatalf("session generation was reused: %d -> %d", before, after)
	}
	// A stale result remains rejected even if a driver delivers it to a later read.
	s.Current().sessionResults = old
	old <- sessionResult{id: 1, generation: before, names: []string{"stale"}}
	s.Consume()
	if s.Current().Databases[0] != "app" {
		t.Fatal("old connection result replaced new metadata")
	}
}

func TestSessionBrowserDatabaseRefreshCancellationFollowsWorkspace(t *testing.T) {
	s, done, notify := connectedState(t)
	started := make(chan context.Context, 1)
	s.DatabaseLister = func(ctx context.Context, _ int64) ([]string, error) {
		started <- ctx
		<-ctx.Done()
		return []string{"stale"}, nil
	}
	s.RefreshDatabases(notify)
	var ctx context.Context
	select {
	case ctx = <-started:
	case <-time.After(time.Second):
		t.Fatal("refresh did not start")
	}
	s.Disconnect(1)
	awaitJob(t, done)
	if ctx.Err() == nil {
		t.Fatal("disconnect did not cancel database-list refresh")
	}
	s.Consume()
	if s.Current().Status != "未连接" || len(s.Current().Databases) != 0 {
		t.Fatal("late refresh republished disconnected metadata")
	}
}

func TestSessionBrowserSecretFallbackAndErrorRedaction(t *testing.T) {
	for _, tc := range []struct {
		name, secret string
		vaultErr     error
		fail         bool
	}{
		{name: "missing keyring", vaultErr: errors.New("keyring unavailable")},
		{name: "redacted connection error", secret: "private-secret", fail: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := New(Dependencies{ReadSecret: func(int64, string) (string, bool, error) { return tc.secret, tc.secret != "", tc.vaultErr }})
			defer s.Close()
			done := make(chan struct{}, 1)
			s.Opener = func(_ context.Context, _ connections.ActiveSettings, password string) error {
				if password != tc.secret {
					return errors.New("unexpected password")
				}
				if tc.fail {
					return errors.New("connect failed: " + password)
				}
				return nil
			}
			s.DatabaseLister = func(context.Context, int64) ([]string, error) { return []string{"app"}, nil }
			s.Connect(testProfile(1), func() { done <- struct{}{} })
			awaitJob(t, done)
			s.Consume()
			if s.Current().Password != "" {
				t.Fatal("session retained credential draft")
			}
			if tc.fail {
				if strings.Contains(s.Current().Error, tc.secret) || !strings.Contains(s.Current().Error, "[已隐藏]") {
					t.Fatal("connection error exposed password")
				}
			} else if s.Current().Status != "已连接" {
				t.Fatal("keyring failure blocked empty-password connection")
			}
		})
	}
}

func TestMySQLWorkbenchAllowsRemoteWithoutTLS(t *testing.T) {
	s := New(Dependencies{})
	defer s.Close()
	profile := testProfile(1)
	profile.Host = "remote.example.test"
	called := make(chan string, 1)
	s.Opener = func(_ context.Context, settings connections.ActiveSettings, _ string) error {
		called <- settings.Host
		return nil
	}
	s.DatabaseLister = func(context.Context, int64) ([]string, error) { return []string{"inventory"}, nil }
	done := make(chan struct{}, 1)
	s.Connect(profile, func() { done <- struct{}{} })
	awaitJob(t, done)
	s.Consume()
	if <-called != profile.Host || s.Current().Status != "已连接" || s.Current().Error != "" || s.Current().Databases[0] != "inventory" {
		t.Fatal("remote plaintext session was blocked or lost metadata")
	}
}

func TestSessionBrowserParkedObjectResultsStayWithTheirProfile(t *testing.T) {
	s, done, notify := connectedState(t)
	s.ObjectLister = func(context.Context, int64, string) ([]mysqlsvc.MySQLDBObject, error) {
		return []mysqlsvc.MySQLDBObject{{Name: "a_orders", Kind: "表"}}, nil
	}
	s.LoadObjects("app", false, notify)
	first := s.Current()
	s.Activate(2)
	second := s.Current()
	second.ProfileID, second.Status = 2, "已连接"
	second.Cache["app"] = []mysqlsvc.MySQLDBObject{{Name: "b_orders", Kind: "表"}}
	second.Databases = []string{"app"}
	awaitJob(t, done)
	s.Consume()
	if len(first.Cache) != 0 || second.Cache["app"][0].Name != "b_orders" {
		t.Fatal("parked object result changed active cache")
	}
	s.Activate(1)
	s.Consume()
	if s.Current() != first || first.Cache["app"][0].Name != "a_orders" || s.CacheFor(2)["app"][0].Name != "b_orders" {
		t.Fatal("restored metadata mixed profile identities")
	}
}
