package sessionbrowser

import (
	"context"
	"testing"

	mysqlsvc "github.com/nyakang/nyadb/internal/services/mysql"
)

func TestNamespaceRefreshPreservesSiblingReadAndOriginalGeneration(t *testing.T) {
	s, done, notify := connectedState(t)
	w := s.Current()
	gate := make(chan struct{})
	defer close(gate)
	s.ObjectLister = func(context.Context, int64, string) ([]mysqlsvc.MySQLDBObject, error) {
		<-gate
		return []mysqlsvc.MySQLDBObject{{Name: "sibling"}}, nil
	}
	s.LoadObjects("other", false, notify)
	request, results, cancel := w.objectRequest, w.objectResults, w.objectCancel
	if s.RefreshNamespaces(w.ProfileID, w.Generation+1, notify) || s.RefreshNamespaces(w.ProfileID+1, w.Generation, notify) {
		t.Fatal("stale namespace refresh accepted")
	}
	if !s.RefreshNamespaces(w.ProfileID, w.Generation, notify) {
		t.Fatal("original namespace refresh refused")
	}
	awaitJob(t, done)
	s.Consume()
	if w.objectRequest != request || w.objectResults != results || w.Pending != "other" || cancel == nil || w.objectCancel == nil {
		t.Fatal("namespace list refresh canceled sibling read")
	}
}
