package dataworkspace

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/graphimport"
	"github.com/nyakang/nyadb/internal/services/neo4j"
)

func TestNeo4jNativePublicGraphImportActualCommitFaultsRetainSourcePreviewAndNeverReplay(t *testing.T) {
	f := neo4jDataFixtureTest(t)
	admin, err := neo4j.Open(context.Background(), f.Config, f.Secret)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	for _, fault := range []string{"lost", "cancel", "close", "user-close", "session-close", "generation"} {
		t.Run(fault, func(t *testing.T) {
			label := "NyaPublicGraphCommit_" + f.Nonce + "_" + strings.ReplaceAll(fault, "-", "_")
			defer neo4jDataRun(t, admin, "MATCH (n) WHERE $label IN labels(n) DETACH DELETE n", map[string]any{"label": label})
			neo4jDataRun(t, admin, "CREATE (n:$($label) {guard:10})", map[string]any{"label": label})
			relay := newNeo4jDataRelay(t, f, string([]byte{0, 2, 0xb0, 0x12, 0, 0}))
			original := f
			original.Config.URI = "bolt://" + relay.listener.Addr().String()
			original.Config.CAFile, original.Config.ClientCertFile, original.Config.ClientKeyFile = "", "", ""
			_, manager, s, active, caps := openNeo4jEntityWorkbench(t, original)
			d := s.OpenObject(1, manager.Namespace(active.ProfileID, "neo4j").Object("label", label), caps)
			s.Load(ui.Services{}, d, false)
			waitNeo4jData(t, s, d)
			s.openDataImport(d)
			bundle := contract.GraphImportBundle{Format: graphimport.Format, Nodes: []contract.GraphNode{{ElementID: "new", Labels: []string{label}, Properties: []contract.GraphProperty{{Name: "guard", Value: contract.GraphValue{Kind: "integer", Text: "999"}}}}}, Relationships: []contract.GraphRelationship{{ElementID: "self", StartElementID: "new", EndElementID: "new", Type: label}}}
			d.transfer.GraphBundle = &bundle
			d.transfer.Step = 1
			s.previewGraphTransfer(ui.Services{}, d)
			settleDocumentTransfer(t, s, d)
			if d.transfer.GraphPlan == nil {
				t.Fatal("public original graph preview missing", d.transfer.Err)
			}
			p, runtime, profile, cached := d.transfer.GraphPlan, s.runtimeFor(d), active.ProfileID, d.page
			d.transfer.Confirmation = "neo4j"
			relay.armed.Store(true)
			s.confirmGraphTransfer(ui.Services{}, d)
			for _, ack := range []<-chan struct{}{relay.observed, relay.responded} {
				select {
				case <-ack:
				case <-time.After(15 * time.Second):
					t.Fatal("public graph COMMIT and withheld acknowledgement missing")
				}
			}
			observed := neo4jDataRun(t, admin, "MATCH (n) WHERE $label IN labels(n) AND n.guard=999 MATCH (n)-[r]->(n) WHERE type(r)=$label RETURN count(r)", map[string]any{"label": label})
			if observed.Rows[0][0].Text != "1" {
				t.Fatal("public graph response fault lacked actual complete commit")
			}
			neo4jDataRun(t, admin, "MATCH (n) WHERE $label IN labels(n) AND n.guard=999 SET n.guard=-789", map[string]any{"label": label})
			queued := make(chan error, 1)
			go func() {
				_, err := runtime.SubmitGraphImport(context.Background(), profile, "neo4j", label, p)
				queued <- err
			}()
			switch fault {
			case "lost":
				relay.dropPairs()
			case "cancel", "user-close":
				s.stopDataTransfer(d)
				if !d.transfer.Busy || !d.transfer.Canceling {
					t.Fatal("graph cancel did not wait for acknowledgement")
				}
			case "close":
				s.Close(d)
			case "session-close":
				if err := manager.Disconnect(profile); err != nil {
					t.Fatal(err)
				}
			case "generation":
				replacement, err := manager.ConnectSession(context.Background(), neo4jDataProfile(t, f), f.Secret)
				if err != nil {
					t.Fatal(err)
				}
				active.Generation = replacement.Generation()
			}
			if fault != "close" {
				waitNeo4jData(t, s, d)
				if d.page != cached || !d.transfer.Uncertain || d.transfer.Completed || d.transfer.GraphBundle == nil || d.transfer.GraphSnapshot.NativeJSON == "" || d.transfer.GraphResult == nil || len(d.transfer.GraphResult.Identities) != 0 || d.edit.Uncertain == nil || !strings.Contains(d.nativeReceipt.JSON, `"CommitConfirmed":false`) {
					t.Fatal("unknown public graph lost source/evidence or admitted generated identity", d.transfer.Err)
				}
				s.confirmGraphTransfer(ui.Services{}, d)
				s.VerifySave(ui.Services{}, d)
				if d.transfer.Busy || d.edit.Busy {
					t.Fatal("unknown graph import allowed recovery or replay")
				}
				if fault == "cancel" {
					captureNeo4jData(t, s, d, "graph-import-unknown", func(c *ui.Context) { s.View(c, d); s.Modals(c) })
				}
			}
			select {
			case err := <-queued:
				if !errors.Is(err, contract.ErrRevoked) && !errors.Is(err, context.Canceled) {
					t.Fatal("queued original graph plan admitted", err)
				}
			case <-time.After(15 * time.Second):
				t.Fatal("queued original graph plan not acknowledged")
			}
			relay.armed.Store(false)
			relay.hold.Store(false)
			if relay.dispatches.Load() != 1 {
				t.Fatal("public graph COMMIT replayed")
			}
			if fault == "user-close" {
				s.stopDataTransfer(d)
				closed := 0
				s.deps.CloseDocument = func(_ ui.Services, original *Document) {
					if original != d || s.GuardLeave(d, nil) {
						t.Fatal("unknown graph close bypassed original guard")
					}
					closed++
					s.Close(d)
				}
				entityTestClick(t, s, d, "关闭原写入文档")
				captureNeo4jData(t, s, d, "graph-import-unknown-close", func(c *ui.Context) { s.nativeWriteReceiptModal(c, d) })
				entityTestClick(t, s, d, "取消")
				if closed != 0 || d.transfer.GraphBundle == nil {
					t.Fatal("canceling unknown graph close discarded source")
				}
				entityTestClick(t, s, d, "关闭原写入文档")
				entityTestClick(t, s, d, "关闭并放弃本地草稿与证据")
				if closed != 1 || !d.closed || d.transfer.GraphBundle != nil || d.nativeReceipt.JSON != "" {
					t.Fatal("explicit unknown graph close retained documents or source")
				}
			}
			check := neo4jDataRun(t, admin, "MATCH (n) WHERE $label IN labels(n) AND n.guard=-789 RETURN count(n)", map[string]any{"label": label})
			if check.Rows[0][0].Text != "1" {
				t.Fatal("original graph replay changed distinctive later state")
			}
			replacement, err := manager.ConnectSession(context.Background(), neo4jDataProfile(t, f), f.Secret)
			if err != nil {
				t.Fatal(err)
			}
			s.Close(d)
			if !manager.IsConnected(profile) || manager.Generation(profile) != replacement.Generation() {
				t.Fatal("original graph cleanup closed replacement")
			}
		})
	}
}
