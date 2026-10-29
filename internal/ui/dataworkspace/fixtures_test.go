package dataworkspace

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/nyakang/nyadb/internal/ui/theme"
)

func dataTestWorkspace(t *testing.T) (*State, *Document, *ui.Tester) {
	t.Helper()
	show := true
	session := Session{ProfileID: 12, Generation: 2, Connected: true}
	s := New(Dependencies{Session: func() Session { return session }, Inspector: InspectorDependencies{Session: func() Session { return session }, Show: func() { show = true }, Hide: func() { show = false }}})
	d := s.Open(1, 12, "app_db", "orders")
	tt := ui.NewTester(func(c *ui.Context) {
		s.Consume(c.Services())
		theme.Apply(c)
		ui.Row(c).Fill().Children(func() {
			ui.Box(c).Grow(1).MinWidth(0).Children(func() { s.View(c, d) })
			if show {
				ui.Box(c).Width(210).Children(func() { s.InspectorView(c, d) })
			}
		})
		s.Modals(c)
	}, 1000, 650)
	return s, d, tt
}
func waitMySQLData(t *testing.T, s *State, d *Document, tt *ui.Tester) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for d.loading && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
		tt.Frame()
	}
	tt.Frame()
	if d.loading {
		t.Fatal("data load did not finish")
	}
}
