package shell

import (
	"math"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestSharedMySQLFormGeometry(t *testing.T) {
	s := New()
	// Test the real child-window geometry, not the compact in-tree fallback.
	s.SetConnectionWindowHost(func() {}, func() {})
	s.openConnectionDialog()
	s.chooseConnectionKind(connections.MySQL)
	s.nextConnectionStep()
	tt := ui.NewTester(s.ConnectionWindowView, 900, 700)

	near := func(a, b float32) bool { return math.Abs(float64(a-b)) <= 1.1 }
	find := func(label string) ui.Rect {
		t.Helper()
		r, ok := tt.Find(label)
		if !ok {
			t.Fatalf("missing form field %q", label)
		}
		return r
	}
	general := []string{"主机", "端口", "用户名", "密码"}
	var prev ui.Rect
	for i, name := range general {
		rect := find(name)
		if !near(rect.X, 314) || !near(rect.H, connectionFormInputHeight) {
			t.Fatalf("general %s geometry: %+v", name, rect)
		}
		if i > 0 && !near(rect.Y-prev.Y, connectionFormRowHeight) {
			t.Fatalf("general row %q no longer uses %g-DIP rhythm: %v vs %v", name, connectionFormRowHeight, rect, prev)
		}
		prev = rect
	}
	if err := tt.Click("高级"); err != nil {
		t.Fatal(err)
	}
	names := []string{"设置位置", "客户端驱动程序", "客户端字符集"}
	for i, name := range names {
		rect := find(name)
		if !near(rect.X, 314) || !near(rect.H, connectionFormInputHeight) {
			t.Fatalf("advanced %s geometry: %+v", name, rect)
		}
		if i > 0 && !near(rect.Y-prev.Y, connectionFormRowHeight) {
			t.Fatalf("advanced row %q off-grid: %+v vs %+v", name, rect, prev)
		}
		prev = rect
	}
	if err := tt.Click("SSL"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"SSL 客户端密钥：", "SSL 客户端证书：", "SSL CA 证书："} {
		rect := find(name)
		if !near(rect.X, 314) || !near(rect.H, connectionFormInputHeight) {
			t.Fatalf("SSL field %s does not share label column: %+v", name, rect)
		}
	}
	if err := tt.Click("SSH"); err != nil {
		t.Fatal(err)
	}
	if r := find("SSH 端口"); !near(r.X, 314) || !near(r.H, connectionFormInputHeight) {
		t.Fatalf("SSH port is off-grid: %+v", r)
	}
	if err := tt.Click("HTTP"); err != nil {
		t.Fatal(err)
	}
	if r := find("HTTP 隧道网址："); !near(r.X, 314) || !near(r.H, connectionFormInputHeight) {
		t.Fatalf("HTTP URL is off-grid: %+v", r)
	}
}
