package shell

import (
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/schemaddl"
	"github.com/nyakang/nyadb/internal/ui/objectworkspace"
)

func TestSchemaRenameForeignReceiptPreservesReplacementAndDraft(t *testing.T) {
	s, tt, services, id := sqliteDropShell(t)
	s.openMySQLData(services, "main", "t")
	d := s.currentTab().data
	waitSQLiteDesigner(t, tt, func() bool { return !d.Loading() })
	s.dataWorkspace.EditCell(services, d, 0, "value", contract.Cell{Kind: contract.Text, Text: "keep draft"})
	target := contract.SchemaDesignTarget{Object: s.runtime.Namespace(id, "main").Object("table", "t"), Generation: s.runtime.Generation(id)}
	event := objectworkspace.SchemaRenameEvent{Original: target, Generation: s.sessions.Current().Generation, Database: "main", Destination: "renamed", Outcome: contract.SchemaDesignOutcome{State: "uncertain"}}
	event.Original.Generation++
	s.schemaTableRenamed(services, event)
	if !s.runtime.IsConnected(id) || !d.HasDraft() || s.currentTab().data != d {
		t.Fatal("stale receipt revoked replacement or lost draft")
	}
	event.Original = target
	s.schemaTableRenamed(services, event)
	if s.runtime.IsConnected(id) || !d.HasDraft() || s.currentTab().data != d {
		t.Fatal("uncertain original receipt lost draft or remained replayable")
	}
}

func TestSchemaRenameCommittedReceiptClosesOnlyOriginalDocumentsAndKeepsActiveID(t *testing.T) {
	s, tt, services, id := sqliteDropShell(t)
	s.openMySQLData(services, "main", "t")
	d := s.currentTab().data
	waitSQLiteDesigner(t, tt, func() bool { return !d.Loading() })
	s.openMySQLStructure(services, "main", "t")
	o := s.currentTab().object
	waitSQLiteDesigner(t, tt, func() bool { return !o.Loading() })
	s.openMySQLData(services, "main", "other")
	other := s.currentTab().data
	waitSQLiteDesigner(t, tt, func() bool { return !other.Loading() })
	activeID := s.currentTab().id
	target := contract.SchemaDesignTarget{Object: s.runtime.Namespace(id, "main").Object("table", "t"), Generation: s.runtime.Generation(id)}
	actual := contract.SchemaDesignSnapshot{Target: target, Schema: schemaddl.Snapshot{Exists: true, Table: schemaddl.Table{Name: "renamed"}}}
	actual.Target.Object.Name = "renamed"
	s.schemaTableRenamed(services, objectworkspace.SchemaRenameEvent{Original: target, Generation: s.sessions.Current().Generation, Database: "main", Destination: "renamed", Outcome: contract.SchemaDesignOutcome{State: "committed", Verified: true, Actual: &actual}})
	for _, tab := range s.tabs {
		if tab.data == d || tab.object == o {
			t.Fatal("original scoped document retained")
		}
	}
	if s.currentTab().id != activeID || s.currentTab().data != other || !s.runtime.IsConnected(id) {
		t.Fatal("inactive cleanup changed active document or session")
	}
}
