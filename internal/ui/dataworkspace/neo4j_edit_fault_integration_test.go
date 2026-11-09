package dataworkspace

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/dbruntime"
	"github.com/nyakang/nyadb/internal/services/neo4j"
	"github.com/nyakang/nyadb/locales"
)

func TestNeo4jNativePublicDataCommitReplyLostCanceledOriginalCloseNeverReplay(t *testing.T) {
	testPublicDataCommitReplyFaults(t, false)
}

func TestNeo4jNativePublicDataExtendedListsCommitReplyLostCanceledOriginalCloseNeverReplay(t *testing.T) {
	testPublicDataCommitReplyFaults(t, true)
}

func testPublicDataCommitReplyFaults(t *testing.T, lists bool) {
	stage := "write-uncertain"
	if lists {
		stage = "extended-list-uncertain"
	}
	f := neo4jDataFixtureTest(t)
	admin, err := neo4j.Open(context.Background(), f.Config, f.Secret)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	label, rel := "NyaGridCommit_"+f.Nonce, "NyaGridCommitRel_"+f.Nonce
	neo4jDataRun(t, admin, "CREATE (a:$($label) {n:1}),(b:$($label)) CREATE (a)-[r:$($rel) {n:1}]->(b)", map[string]any{"label": label, "rel": rel})
	defer neo4jDataRun(t, admin, "MATCH (n) WHERE $label IN labels(n) DETACH DELETE n", map[string]any{"label": label})
	for _, kind := range []string{"label", "relationship"} {
		for _, fault := range []string{"lost", "cancel", "close"} {
			t.Run(kind+"/"+fault, func(t *testing.T) {
				name, pattern := label, "(e)"
				if kind == "relationship" {
					name, pattern = rel, "()-[e]->()"
				}
				neo4jDataRun(t, admin, "MATCH "+pattern+" WHERE "+map[string]string{"label": "$name IN labels(e)", "relationship": "type(e)=$name"}[kind]+" SET e.n=1", map[string]any{"name": name})
				if lists {
					neo4jDataRun(t, admin, "MATCH "+pattern+" WHERE "+map[string]string{"label": "$name IN labels(e)", "relationship": "type(e)=$name"}[kind]+gridExtendedListAssignments("e"), map[string]any{"name": name})
				}
				// Exact native Bolt COMMIT: two-byte chunk, zero-field structure 0x12,
				// end-of-message. The test has no binary inputs containing this frame.
				relay := newNeo4jDataRelay(t, f, string([]byte{0, 2, 0xb0, 0x12, 0, 0}))
				original := f
				original.Config.URI = "bolt://" + relay.listener.Addr().String()
				original.Config.CAFile, original.Config.ClientCertFile, original.Config.ClientKeyFile = "", "", ""
				profile := neo4jDataProfile(t, original)
				manager := dbruntime.New()
				dbruntime.RegisterBuiltins(manager, nil)
				defer manager.Close()
				handle, err := manager.ConnectSession(context.Background(), profile, f.Secret)
				if err != nil {
					t.Fatal(err)
				}
				active := Session{ProfileID: profile.ID, Generation: handle.Generation(), Connected: true}
				s := New(Dependencies{Session: func() Session { return active }, RuntimeFor: manager.Data})
				defer s.CloseAll()
				object := manager.Namespace(profile.ID, "neo4j").Object(kind, name)
				d := s.OpenObject(1, object, handle.Capabilities())
				userClosed := 0
				s.deps.CloseDocument = func(_ ui.Services, original *Document) {
					if original != d || original.Object() != object || s.GuardLeave(original, nil) {
						t.Fatal("native user close lost its original qualified guard")
					}
					userClosed++
					s.Close(original)
				}
				s.Load(ui.Services{}, d, false)
				waitNeo4jData(t, s, d)
				col := slices.Index(d.page.Columns, "property.n")
				s.beginMySQLCellEdit(ui.Services{}, d, 0, col, false)
				d.edit.Editor.Text = "9223372036854775807"
				s.finishMySQLCellEdit(d, true)
				if lists {
					for _, sample := range gridExtendedListSamples() {
						raw, _ := json.Marshal(gridExtendedListValue(t, sample))
						s.EditCell(ui.Services{}, d, 0, "property.list_"+sample.kind, contract.Cell{Kind: contract.JSON, Text: string(raw)})
					}
				}
				key := mysqlDataRowKey(d, 0)
				edits := []contract.DataEdit{{Kind: "update", Original: cloneSnapshot(d.edit.Drafts[key].Original), Values: cloneValues(d.edit.Drafts[key].Values)}}
				runtime := s.runtimeFor(d)
				cached := d.page
				relay.armed.Store(true)
				s.saveMySQLData(ui.Services{}, d)
				select {
				case <-relay.observed:
				case <-time.After(15 * time.Second):
					t.Fatal("actual native COMMIT frame not forwarded")
				}
				select {
				case <-relay.responded:
				case <-time.After(15 * time.Second):
					t.Fatal("actual native COMMIT response not withheld")
				}
				id := edits[0].Original.Ref.TypedValues[0].Text
				read := neo4jDataRun(t, admin, "MATCH "+pattern+" WHERE elementId(e)=$id RETURN e.n", map[string]any{"id": id})
				if read.Rows[0][0].Text != "9223372036854775807" {
					t.Fatal("withheld COMMIT response lacked independently confirmed native commit")
				}
				if lists {
					complete := neo4jDataRun(t, admin, "MATCH "+pattern+" WHERE elementId(e)=$id RETURN e", map[string]any{"id": id})
					assertGridExtendedLists(t, complete.Rows[0][0])
				}
				neo4jDataRun(t, admin, "MATCH "+pattern+" WHERE elementId(e)=$id SET e.n=-789", map[string]any{"id": id})
				queued := make(chan error, 1)
				go func() { _, e := runtime.Saver(context.Background(), profile.ID, "neo4j", name, edits); queued <- e }()
				switch fault {
				case "lost":
					relay.dropPairs()
				case "cancel":
					s.stopMySQLDataEdit(d)
					if !d.edit.Busy || d.edit.Results == nil {
						t.Fatal("save cancellation settled before executor acknowledgement")
					}
				case "close":
					s.Close(d)
				}
				if fault != "close" {
					waitNeo4jData(t, s, d)
					if d.edit.Uncertain == nil || !d.edit.Uncertain.CommitUncertain || len(d.edit.Drafts) != 1 || d.page != cached || !strings.Contains(d.nativeReceipt.JSON, `"CommitAttempted":true`) || !strings.Contains(d.nativeReceipt.JSON, `"CommitConfirmed":false`) {
						t.Fatal("uncertain native commit replaced drafts or lost original receipt", d.edit.Err)
					}
					if kind == "label" && fault == "cancel" {
						captureNeo4jData(t, s, d, stage+"-commit")
					}
					s.saveMySQLData(ui.Services{}, d)
					s.verifyMySQLDataSave(ui.Services{}, d)
					if d.edit.Busy {
						t.Fatal("unknown native commit admitted replay/readback recovery")
					}
				}
				select {
				case err = <-queued:
					if !errors.Is(err, contract.ErrRevoked) && !errors.Is(err, context.Canceled) {
						t.Fatal("queued uncertain write did not stop before dispatch", err)
					}
				case <-time.After(15 * time.Second):
					t.Fatal("queued writer did not acknowledge original close/failure")
				}
				relay.armed.Store(false)
				relay.hold.Store(false)
				if fault != "close" {
					count, err := runtime.Count(context.Background(), profile.ID, "neo4j", name)
					if err != nil || count < 1 {
						t.Fatal("uncertain write retired independent read scope", err)
					}
				}
				again := neo4jDataRun(t, admin, "MATCH "+pattern+" WHERE elementId(e)=$id RETURN e.n", map[string]any{"id": id})
				if again.Rows[0][0].Text != "-789" || relay.dispatches.Load() != 1 {
					t.Fatal("native original COMMIT replayed over distinctive later value")
				}

				if kind == "label" && fault == "cancel" {
					tester := ui.NewTester(func(c *ui.Context) { s.View(c, d); s.Modals(c) }, 1280, 800)
					if err := tester.Click(locales.Text("关闭原写入文档")); err != nil {
						t.Fatal(err)
					}
					captureNeo4jData(t, s, d, stage+"-close", func(c *ui.Context) { s.nativeWriteReceiptModal(c, d) })
					if err := tester.Click(locales.Text("取消")); err != nil {
						t.Fatal(err)
					}
					if userClosed != 0 || !s.GuardLeave(d, nil) || len(d.edit.Drafts) != 1 {
						t.Fatal("canceling native close discarded or unblocked original draft")
					}
					if err := tester.Click(locales.Text("关闭原写入文档")); err != nil {
						t.Fatal(err)
					}
					if err := tester.Click(locales.Text("关闭并放弃本地草稿与证据")); err != nil {
						t.Fatal(err)
					}
					if userClosed != 1 || !d.closed || d.nativeReceipt.JSON != "" || d.edit.Uncertain != nil {
						t.Fatal("native original close did not release original evidence")
					}
				}
				replacement, err := manager.ConnectSession(context.Background(), neo4jDataProfile(t, f), f.Secret)
				if err != nil {
					t.Fatal(err)
				}
				s.Close(d)
				handle.Close()
				if !manager.IsConnected(profile.ID) || manager.Generation(profile.ID) != replacement.Generation() {
					t.Fatal("original graph write cleanup closed replacement generation")
				}
			})
		}
	}
}
