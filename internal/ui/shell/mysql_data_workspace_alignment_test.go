package shell

import (
	"testing"
)

func TestMySQLWorkspaceCaptionsKeepQualifiedTabIdentity(t *testing.T) {
	s := New()
	data := &tab{kind: tabMySQLData, title: "first.orders · 数据", data: newDataDocument(s, 1, "first", "orders")}
	design := &tab{kind: tabStructure, title: "first.orders",
		object: s.objectWorkspace.OpenStructure(1, 1, "first", "orders")}
	s.tabs = []*tab{data, design}
	if s.workspaceTabCaption(data) != "orders" || s.workspaceTabCaption(design) != "orders · 设计" {
		t.Fatalf("compact tab captions lost data/design distinction")
	}
	other := &tab{kind: tabMySQLData, title: "second.orders · 数据", data: newDataDocument(s, 1, "second", "orders")}
	s.tabs = append(s.tabs, other)
	if s.workspaceTabCaption(data) != "first.orders" ||
		s.workspaceTabCaption(design) != "first.orders · 设计" ||
		s.workspaceTabCaption(other) != "second.orders" {
		t.Fatalf("cross-database same-name tabs were ambiguous")
	}
	if data.title != "first.orders · 数据" || other.title != "second.orders · 数据" {
		t.Fatal("caption change modified qualified tab identity or close label")
	}
}
