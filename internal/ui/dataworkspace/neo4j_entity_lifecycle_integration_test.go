package dataworkspace

import (
	"context"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/neo4j"
)

func TestNeo4jNativePublicDataEntityPreviewParkGenerationCancelAndOriginalClose(t *testing.T) {
	f := neo4jDataFixtureTest(t)
	direct, err := neo4j.Open(context.Background(), f.Config, f.Secret)
	if err != nil {
		t.Fatal(err)
	}
	defer direct.Close()
	for _, phase := range []string{"park", "generation", "unconsumed-close", "late-cancel", "late-close"} {
		t.Run(phase, func(t *testing.T) {
			label := "NyaPublicEntityLife_" + f.Nonce + "_" + phase
			neo4jDataRun(t, direct, "CREATE (a:$($label) {n:1}),(b:$($label) {n:2}) CREATE (a)-[:NYA_PUBLIC_LIFE {n:1}]->(b)", map[string]any{"label": label})
			defer neo4jDataRun(t, direct, "MATCH (n) WHERE $label IN labels(n) DETACH DELETE n", map[string]any{"label": label})
			relay := newNeo4jDataRelay(t, f, "AS relationships LIMIT 2")
			original := f
			original.Config.URI = "bolt://" + relay.listener.Addr().String()
			original.Config.CAFile, original.Config.ClientCertFile, original.Config.ClientKeyFile = "", "", ""
			_, manager, s, active, caps := openNeo4jEntityWorkbench(t, original)
			object := manager.Namespace(active.ProfileID, "neo4j").Object("label", label)
			d := s.OpenObject(1, object, caps)
			s.Load(ui.Services{}, d, false)
			waitNeo4jData(t, s, d)
			d.selectedRow = 0
			d.selection.Add(0)
			s.deleteMySQLDataRecords(ui.Services{}, d, 0)
			s.chooseDataDeleteKind(ui.Services{}, d, "detach_delete")
			if phase == "late-cancel" || phase == "late-close" {
				relay.armed.Store(true)
			}
			s.Save(ui.Services{}, d)
			ticket, results := d.mutation.ticket, d.mutation.results
			if phase == "late-cancel" || phase == "late-close" {
				select {
				case <-relay.observed:
				case <-time.After(15 * time.Second):
					t.Fatal("original entity preview request not observed")
				}
				select {
				case <-relay.responded:
				case <-time.After(15 * time.Second):
					t.Fatal("original entity preview response not withheld")
				}
				if phase == "late-close" {
					s.Close(d)
				} else {
					s.stopMySQLDataEdit(d)
					if !d.edit.Busy || d.mutation.results == nil {
						t.Fatal("preview cancellation preceded native executor acknowledgement")
					}
					deadline := time.Now().Add(30 * time.Second)
					for d.mutation.results != nil && time.Now().Before(deadline) {
						s.Consume(ui.Services{})
						time.Sleep(time.Millisecond)
					}
					if d.mutation.results != nil {
						t.Fatal("original native preview cancellation acknowledgement stalled")
					}
					if d.edit.Busy || d.edit.Uncertain != nil || !d.HasDraft() {
						t.Fatal("canceled preview lost drafts or became unknown write")
					}
				}
				if phase == "late-close" {
					select {
					case <-results:
					case <-time.After(15 * time.Second):
						t.Fatal("closed original preview did not acknowledge cancellation")
					}
				}
				if ticket.take() != nil {
					t.Fatal("canceled/closed native preview retained a late authorization")
				}
			} else {
				// Finish a genuine native preview without consuming its UI notification.
				select {
				case e := <-results:
					if e != nil {
						t.Fatal(e)
					}
					out := make(chan error, 1)
					out <- e
					d.mutation.results = out
				case <-time.After(15 * time.Second):
					t.Fatal("native preview stalled")
				}
				ticket.mu.Lock()
				plan := ticket.plan
				ticket.mu.Unlock()
				if plan == nil || plan.Snapshot().Object != object {
					t.Fatal("native original preview not published")
				}
				profile := active.ProfileID
				if phase == "unconsumed-close" {
					s.Close(d)
					if ticket.take() != nil || plan.Snapshot().Object != (contract.ObjectRef{}) || d.mutation.snapshot.NativeJSON != "" {
						t.Fatal("close retained an unconsumed native plan")
					}
				} else {
					active.ProfileID = 99
					s.Consume(ui.Services{})
					if d.mutation.open || !d.edit.Busy || !d.HasDraft() {
						t.Fatal("parked native preview consumed by another profile")
					}
					if phase == "generation" {
						replacement, e := manager.ConnectSession(context.Background(), neo4jDataProfile(t, f), f.Secret)
						if e != nil {
							t.Fatal(e)
						}
						active.ProfileID = profile
						s.Consume(ui.Services{})
						if d.edit.Busy || d.edit.Uncertain != nil || !d.HasDraft() || plan.Snapshot().Object != (contract.ObjectRef{}) {
							t.Fatal("late old-generation preview authorized a write or lost drafts")
						}
						s.Close(d)
						if manager.Generation(profile) != replacement.Generation() || !manager.IsConnected(profile) {
							t.Fatal("late original preview closed replacement publication")
						}
					} else {
						active.ProfileID = profile
						waitNeo4jMutationPreview(t, s, d)
						captureNeo4jData(t, s, d, "entity-preview-parked", func(c *ui.Context) { s.View(c, d); s.Modals(c) })
						entityTestClick(t, s, d, "取消")
						if d.edit.Busy || !d.HasDraft() || plan.Snapshot().Object != (contract.ObjectRef{}) {
							t.Fatal("returned original preview cancellation retained plan or lost drafts")
						}
					}
				}
			}
			relay.armed.Store(false)
			relay.hold.Store(false)
			observed := neo4jDataRun(t, direct, "MATCH (n) WHERE $label IN labels(n) RETURN count(n)", map[string]any{"label": label})
			if observed.Rows[0][0].Text != "2" {
				t.Fatal("preview lifecycle wrote or deleted a live node")
			}
			s.CloseAll()
			manager.Close()
		})
	}
}
