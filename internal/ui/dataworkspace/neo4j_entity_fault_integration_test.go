package dataworkspace

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/neo4j"
)

func TestNeo4jNativePublicDataEntityCommitReplyLostCanceledOriginalCloseNeverReplay(t *testing.T) {
	f := neo4jDataFixtureTest(t)
	admin, err := neo4j.Open(context.Background(), f.Config, f.Secret)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	for _, operation := range []string{"insert", "delete", "detach_delete", "relationship_delete"} {
		for _, fault := range []string{"lost", "cancel", "close", "user-close"} {
			t.Run(operation+"/"+fault, func(t *testing.T) {
				label := "NyaPublicEntityCommit_" + f.Nonce + "_" + operation + "_" + strings.ReplaceAll(fault, "-", "_")
				outside, rel := label+"_outside", label+"_rel"
				defer neo4jDataRun(t, admin, "MATCH (n) WHERE $label IN labels(n) OR $outside IN labels(n) DETACH DELETE n", map[string]any{"label": label, "outside": outside})
				if operation == "relationship_delete" {
					neo4jDataRun(t, admin, "CREATE (a:$($label) {n:1}),(b:$($label) {n:2}) CREATE (a)-[:$($rel) {n:1}]->(b)", map[string]any{"label": label, "rel": rel})
				} else if operation == "detach_delete" {
					neo4jDataRun(t, admin, "CREATE (a:$($label) {n:1}),(b:$($outside) {n:2}) CREATE (a)-[:$($rel) {n:1}]->(b)", map[string]any{"label": label, "outside": outside, "rel": rel})
				} else {
					neo4jDataRun(t, admin, "CREATE (a:$($label) {n:1})", map[string]any{"label": label})
				}
				relay := newNeo4jDataRelay(t, f, string([]byte{0, 2, 0xb0, 0x12, 0, 0}))
				original := f
				original.Config.URI = "bolt://" + relay.listener.Addr().String()
				original.Config.CAFile, original.Config.ClientCertFile, original.Config.ClientKeyFile = "", "", ""
				_, manager, s, active, caps := openNeo4jEntityWorkbench(t, original)
				kind, name := "label", label
				if operation == "relationship_delete" {
					kind, name = "relationship", rel
				}
				object := manager.Namespace(active.ProfileID, "neo4j").Object(kind, name)
				d := s.OpenObject(1, object, caps)
				s.Load(ui.Services{}, d, false)
				waitNeo4jData(t, s, d)
				if operation == "insert" {
					s.addMySQLDataRecord(ui.Services{}, d)
					s.finishMySQLCellEdit(d, false)
					s.EditCell(ui.Services{}, d, len(d.page.Rows)-1, "property.n", contract.Cell{Kind: contract.Integer, Text: "999"})
				} else {
					d.selectedRow = 0
					d.selection.Add(0)
					s.deleteMySQLDataRecords(ui.Services{}, d, 0)
					if d.deleteChoice != nil {
						chosen := "delete"
						if operation == "detach_delete" {
							chosen = "detach_delete"
						}
						s.chooseDataDeleteKind(ui.Services{}, d, chosen)
					}
				}
				cached := d.page
				s.Save(ui.Services{}, d)
				waitNeo4jMutationPreview(t, s, d)
				plan, runtime := d.mutation.plan, s.runtimeFor(d)
				relay.armed.Store(true)
				s.confirmDataMutation(ui.Services{}, d)
				for _, ack := range []<-chan struct{}{relay.observed, relay.responded} {
					select {
					case <-ack:
					case <-time.After(15 * time.Second):
						t.Fatal("actual entity COMMIT/response not observed")
					}
				}
				// Independent observations prove actual COMMIT, then establish a distinct
				// later state which any unintended replay would overwrite or remove.
				if operation == "insert" {
					observed := neo4jDataRun(t, admin, "MATCH (n) WHERE $label IN labels(n) AND n.n=999 RETURN count(n)", map[string]any{"label": label})
					if observed.Rows[0][0].Text != "1" {
						t.Fatal("withheld entity reply lacked actual node creation")
					}
					neo4jDataRun(t, admin, "MATCH (n) WHERE $label IN labels(n) AND n.n=999 SET n.n=-789", map[string]any{"label": label})
				} else if operation == "relationship_delete" {
					observed := neo4jDataRun(t, admin, "MATCH ()-[r]->() WHERE type(r)=$rel RETURN count(r)", map[string]any{"rel": rel})
					if observed.Rows[0][0].Text != "0" {
						t.Fatal("withheld entity reply lacked actual relationship deletion")
					}
					neo4jDataRun(t, admin, "MATCH (a),(b) WHERE $label IN labels(a) AND $label IN labels(b) AND a.n=1 AND b.n=2 CREATE (a)-[:$($rel) {n:-789}]->(b)", map[string]any{"label": label, "rel": rel})
				} else {
					observed := neo4jDataRun(t, admin, "MATCH (n) WHERE $label IN labels(n) RETURN count(n)", map[string]any{"label": label})
					if observed.Rows[0][0].Text != "0" {
						t.Fatal("withheld entity reply lacked actual node deletion")
					}
					if operation == "detach_delete" {
						neo4jDataRun(t, admin, "MATCH (b) WHERE $outside IN labels(b) CREATE (a:$($label) {n:-789}) CREATE (a)-[:$($rel) {n:-789}]->(b)", map[string]any{"label": label, "outside": outside, "rel": rel})
					} else {
						neo4jDataRun(t, admin, "CREATE (a:$($label) {n:-789})", map[string]any{"label": label})
					}
				}
				queued := make(chan error, 1)
				profile := active.ProfileID
				go func() { _, e := runtime.SubmitEdits(context.Background(), profile, "neo4j", name, plan); queued <- e }()
				switch fault {
				case "lost":
					relay.dropPairs()
				case "cancel", "user-close":
					s.stopMySQLDataEdit(d)
					if !d.edit.Busy || d.edit.Results == nil {
						t.Fatal("entity cancellation preceded original executor acknowledgement")
					}
				case "close":
					s.Close(d)
				}
				if fault != "close" {
					waitNeo4jData(t, s, d)
					if d.page != cached || !d.HasDraft() || d.edit.Uncertain == nil || !d.edit.Uncertain.CommitUncertain || !strings.Contains(d.nativeReceipt.JSON, `"CommitAttempted":true`) || !strings.Contains(d.nativeReceipt.JSON, `"CommitConfirmed":false`) {
						t.Fatal("unknown entity commit lost drafts/evidence or accepted provisional rows", d.edit.Err)
					}
					if operation == "insert" && d.page.RowRefs[len(d.page.Rows)-1] != nil {
						t.Fatal("unknown entity creation accepted provisional generated identity")
					}
					s.Save(ui.Services{}, d)
					s.VerifySave(ui.Services{}, d)
					if d.edit.Busy {
						t.Fatal("unknown entity commit admitted replay/readback recovery")
					}
					if operation == "insert" && fault == "cancel" {
						captureNeo4jData(t, s, d, "entity-commit-unknown")
					}
				}
				select {
				case err = <-queued:
					if !errors.Is(err, contract.ErrRevoked) && !errors.Is(err, context.Canceled) {
						t.Fatal("queued original entity plan not rejected", err)
					}
				case <-time.After(15 * time.Second):
					t.Fatal("queued entity submission did not acknowledge original revocation")
				}
				relay.armed.Store(false)
				relay.hold.Store(false)
				if relay.dispatches.Load() != 1 {
					t.Fatal("native entity COMMIT replayed")
				}
				if operation == "relationship_delete" {
					observed := neo4jDataRun(t, admin, "MATCH ()-[r]->() WHERE type(r)=$rel RETURN r.n", map[string]any{"rel": rel})
					if len(observed.Rows) != 1 || observed.Rows[0][0].Text != "-789" {
						t.Fatal("queued entity operation changed distinctive later relationship")
					}
				} else {
					observed := neo4jDataRun(t, admin, "MATCH (n) WHERE $label IN labels(n) AND n.n=-789 RETURN count(n)", map[string]any{"label": label})
					if observed.Rows[0][0].Text != "1" {
						t.Fatal("queued entity operation changed distinctive later node")
					}
				}
				if fault == "user-close" {
					closed := 0
					s.deps.CloseDocument = func(_ ui.Services, original *Document) {
						if original != d || original.Object() != object || s.GuardLeave(original, nil) {
							t.Fatal("entity unknown close lost original qualified guard")
						}
						closed++
						s.Close(original)
					}
					entityTestClick(t, s, d, "关闭原写入文档")
					if operation == "insert" {
						captureNeo4jData(t, s, d, "entity-commit-unknown-close", func(c *ui.Context) { s.nativeWriteReceiptModal(c, d) })
					}
					entityTestClick(t, s, d, "取消")
					if closed != 0 || !d.HasDraft() {
						t.Fatal("unknown entity close cancellation lost original drafts")
					}
					entityTestClick(t, s, d, "关闭原写入文档")
					entityTestClick(t, s, d, "关闭并放弃本地草稿与证据")
					if closed != 1 || !d.closed || d.page != nil || d.nativeReceipt.JSON != "" {
						t.Fatal("explicit original entity close retained caches or receipts")
					}
				}
				replacement, err := manager.ConnectSession(context.Background(), neo4jDataProfile(t, f), f.Secret)
				if err != nil {
					t.Fatal(err)
				}
				s.Close(d)
				if !manager.IsConnected(profile) || manager.Generation(profile) != replacement.Generation() {
					t.Fatal("old entity cleanup closed replacement publication")
				}
				s.CloseAll()
				manager.Close()
			})
		}
	}
}
