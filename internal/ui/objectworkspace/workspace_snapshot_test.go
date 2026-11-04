package objectworkspace

import (
	"encoding/json"
	"testing"
)

func TestRestoredBrowserWaitsForOriginalDatabase(t *testing.T) {
	snapshot := BrowserSnapshot{ProfileID: 7}
	s := New(Dependencies{Browser: func() BrowserSnapshot { return snapshot }})
	saved, _ := json.Marshal([]browserPresentation{{Profile: 7, Database: "app", Kind: "视图", Category: "表", Row: 2, ScrollY: 80}})
	if err := s.RestoreWorkspace(saved); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		s.currentBrowser()
	}
	b := s.presentation(7)
	if b.Kind != "视图" || b.Row != 2 || b.Scroll.Y != 80 {
		t.Fatal("disconnected frame reset the scene")
	}
	snapshot.Database, snapshot.Connected = "app", true
	s.currentBrowser()
	if b.restored != nil || b.Kind != "视图" || b.Row != 2 || b.Scroll.Y != 80 {
		t.Fatal("original database did not restore the scene")
	}
	snapshot.Database = "other"
	s.currentBrowser()
	if b.Kind != "全部" || b.Row != -1 {
		t.Fatal("explicit database change did not reset selection")
	}
}
