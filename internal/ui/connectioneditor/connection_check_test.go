package connectioneditor

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func newMySQLConnectionTestUI(t *testing.T) (*testEditor, *ui.Tester) {
	t.Helper()
	s := newTestEditor()
	s.openConnectionDialog()
	s.chooseConnectionKind(connections.MySQL)
	s.nextConnectionStep()
	s.connectionDraft.Name = "Loopback test"
	s.connectionDraft.Host = "127.0.0.1"
	return s, ui.NewTester(s.ConnectionWindowView, 900, 700)
}

func TestMySQLConnectionTestAsyncResultAndSecretRedaction(t *testing.T) {
	s, tt := newMySQLConnectionTestUI(t)
	entered := make(chan struct{}, 1)
	complete := make(chan struct{})
	s.mysqlConnectionTester = func(ctx context.Context, c connections.ActiveSettings, password string) error {
		if c.Host != "127.0.0.1" || password != "top-secret" {
			return errors.New("incorrect request")
		}
		entered <- struct{}{}
		<-complete
		return nil
	}
	s.connectionSecret = "top-secret"
	if err := tt.Click("测试当前 MySQL 配置"); err != nil {
		t.Fatal(err)
	}
	if !s.connectionTesting || !tt.HasText("取消测试") {
		t.Fatal("test did not start asynchronously")
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("background runner was not started")
	}
	close(complete)
	deadline := time.Now().Add(2 * time.Second)
	for s.connectionTesting && time.Now().Before(deadline) {
		tt.Frame()
		time.Sleep(5 * time.Millisecond)
	}
	if s.connectionTesting || !strings.Contains(s.connectionTestMessage, "成功") ||
		!tt.HasText("测试连接") {
		t.Fatalf("completion not delivered to UI: %q", s.connectionTestMessage)
	}
	// Test was a probe. It did not save connection credentials or close editor.
	if !s.connectionDialog || s.connectionSecret != "top-secret" {
		t.Fatal("successful probe modified editor or credentials")
	}
	s.mysqlConnectionTester = func(context.Context, connections.ActiveSettings, string) error {
		return errors.New("driver: denied top-secret")
	}
	if err := tt.Click("测试当前 MySQL 配置"); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(2 * time.Second)
	for s.connectionTesting && time.Now().Before(deadline) {
		tt.Frame()
		time.Sleep(5 * time.Millisecond)
	}
	if s.connectionTesting || strings.Contains(s.connectionError, "top-secret") ||
		!strings.Contains(s.connectionError, "[已隐藏]") {
		t.Fatalf("driver error leaked password: %q", s.connectionError)
	}
}

func TestMySQLConnectionTestCancelDiscardLateResultAndReopen(t *testing.T) {
	s, tt := newMySQLConnectionTestUI(t)
	started := make(chan struct{})
	release := make(chan struct{})
	canceled := make(chan struct{})
	s.mysqlConnectionTester = func(ctx context.Context, _ connections.ActiveSettings, _ string) error {
		close(started)
		<-ctx.Done()
		close(canceled)
		<-release
		return ctx.Err()
	}
	s.connectionSecret = "not-saved"
	if err := tt.Click("测试当前 MySQL 配置"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("no runner")
	}
	if err := tt.Click("取消测试"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("Cancel did not cancel context")
	}
	if s.connectionTesting || !strings.Contains(s.connectionTestMessage, "取消") {
		t.Fatal("Cancel did not update UI")
	}
	s.closeConnectionDialog()
	close(release)
	s.openConnectionDialog()
	s.chooseConnectionKind(connections.MySQL)
	s.nextConnectionStep()
	tt.Frame()
	if s.connectionTestResults != nil || s.connectionTesting || s.connectionSecret != "" ||
		s.connectionTestMessage != "" {
		t.Fatal("stale test result or credential escaped across windows")
	}
}

func TestMySQLConnectionTestUnsupportedOptionsNoNetwork(t *testing.T) {
	s, tt := newMySQLConnectionTestUI(t)
	called := false
	s.mysqlConnectionTester = func(context.Context, connections.ActiveSettings, string) error {
		called = true
		return nil
	}
	s.connectionDraft.MySQLAdvanced.SSL.Enabled = true
	s.connectionDraft.MySQLAdvanced.SSL.ClientKey = "missing-client-key.pem"
	if err := tt.Click("测试当前 MySQL 配置"); err != nil {
		t.Fatal(err)
	}
	if called || s.connectionTesting || !strings.Contains(s.connectionError, "certificate") {
		t.Fatalf("unsupported encryption was not rejected: %q", s.connectionError)
	}
	s.closeConnectionDialog()
	if s.connectionTesting {
		t.Fatal("closed editor retained test")
	}
}
