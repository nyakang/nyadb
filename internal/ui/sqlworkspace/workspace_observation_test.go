package sqlworkspace

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/queries"
)

func TestCancellationAndSessionLossImmediatelyRequireRecovery(t *testing.T) {
	for _, lost := range []error{contract.ErrSQLSessionLost, contract.ErrRevoked} {
		s := New(Dependencies{})
		d := s.Open(2, "SELECT retained", 19, "db", false)
		d.loading, d.cancelRequested, d.request = true, true, 1
		out := make(chan executionResult, 1)
		out <- executionResult{request: 1, profileID: 19, err: errors.Join(context.Canceled, lost)}
		d.results = out
		s.Consume()
		if !d.NeedsRecovery() || d.Loading() || d.recoveryProfile != 19 || d.recoverySchema != "db" || !strings.Contains(d.err, "服务器端更改可能已生效") || d.Editor.Buffer.String() != "SELECT retained" {
			t.Fatal("cancellation hid session loss or changed SQL")
		}
		s.CloseAll()
	}
}

func TestWorkspaceObservationTracksMetadataAndOwnsMutableBuffers(t *testing.T) {
	s := New(Dependencies{})
	defer s.CloseAll()
	s.persistence.store = &queries.Store{}
	w := queries.Workspace{SnapshotVersion: 2, ActiveID: 2, Shell: []byte(`{"n":1}`),
		Documents: []queries.Document{{ID: 2, Text: "SELECT 1", Presentation: []byte(`{"n":1}`)}},
		Tabs:      []queries.WorkspaceTab{{ID: 2, Title: "SQL", Presentation: []byte(`{"n":1}`)}}}
	s.ObserveWorkspace(w)
	s.ObserveWorkspace(w)
	if s.persistence.revision != 1 {
		t.Fatal("unchanged observation produced a revision")
	}
	w.Shell[5], w.Documents[0].Presentation[5], w.Tabs[0].Presentation[5] = '2', '2', '2'
	w.Tabs[0].Title = "changed"
	if string(s.persistence.lastWorkspace.Shell) != `{"n":1}` || string(s.persistence.lastWorkspace.Documents[0].Presentation) != `{"n":1}` || s.persistence.lastWorkspace.Tabs[0].Title != "SQL" {
		t.Fatal("caller mutated captured snapshot")
	}
	changes := []func(){
		func() { w.Documents[0].SavedText = "saved" },
		func() { w.Documents[0].NamedRevision++ },
		func() { w.Documents[0].Cursor++ },
		func() {
			w.Documents[0].ProfileID = 7
			w.Documents[0].Catalog = "catalog"
			w.Documents[0].Schema = "schema"
		},
		func() { w.Tabs[0].Pinned = true },
		func() { w.ActiveID = 1 },
	}
	s.ObserveWorkspace(w)
	for _, change := range changes {
		before := s.persistence.revision
		change()
		s.ObserveWorkspace(w)
		if s.persistence.revision != before+1 {
			t.Fatal("lost presentation or document change")
		}
		s.ObserveWorkspace(w)
		if s.persistence.revision != before+1 {
			t.Fatal("repeated unchanged revision")
		}
	}
	w.Revision = 999
	before := s.persistence.revision
	s.ObserveWorkspace(w)
	if s.persistence.revision != before {
		t.Fatal("persistence revision changed scene identity")
	}
	s.persistence.changed = time.Time{}
}

func BenchmarkUnchangedWorkspaceObserve(b *testing.B) {
	for _, size := range []int{1 << 20, 10 << 20} {
		b.Run(map[int]string{1 << 20: "1MiB", 10 << 20: "10MiB"}[size], func(b *testing.B) {
			s := New(Dependencies{})
			s.persistence.store = &queries.Store{}
			w := queries.Workspace{SnapshotVersion: 2, Documents: []queries.Document{{ID: 2, Text: strings.Repeat("x", size)}}}
			s.ObserveWorkspace(w)
			s.persistence.changed = time.Time{}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				s.ObserveWorkspace(w)
			}
		})
	}
}
