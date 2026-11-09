package dataworkspace

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/graphimport"
	"github.com/nyakang/nyadb/internal/services/neo4j"
)

func TestNeo4jNativePublicGraphImportPreviewParkGenerationCancelAndOriginalClose(t *testing.T) {
	f := neo4jDataFixtureTest(t)
	admin, err := neo4j.Open(context.Background(), f.Config, f.Secret)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	for _, phase := range []string{"park", "generation", "unconsumed-close", "late-cancel", "late-close"} {
		t.Run(phase, func(t *testing.T) {
			label := "NyaPublicGraphLife_" + f.Nonce + "_" + strings.ReplaceAll(phase, "-", "_")
			defer neo4jDataRun(t, admin, "MATCH (n) WHERE $label IN labels(n) DETACH DELETE n", map[string]any{"label": label})
			anchor := neo4jDataRun(t, admin, "CREATE (n:$($label) {keep:1}) RETURN n", map[string]any{"label": label}).Rows[0][0].Node.ElementID
			relay := newNeo4jDataRelay(t, f, "UNWIND $ids AS id MATCH (entity) WHERE elementId(entity)=id RETURN entity ORDER BY elementId(entity)")
			original := f
			original.Config.URI = "bolt://" + relay.listener.Addr().String()
			original.Config.CAFile, original.Config.ClientCertFile, original.Config.ClientKeyFile = "", "", ""
			_, manager, s, active, caps := openNeo4jEntityWorkbench(t, original)
			d := s.OpenObject(1, manager.Namespace(active.ProfileID, "neo4j").Object("label", label), caps)
			s.Load(ui.Services{}, d, false)
			waitNeo4jData(t, s, d)
			s.openDataImport(d)
			bundle := contract.GraphImportBundle{Format: graphimport.Format, Nodes: []contract.GraphNode{{ElementID: "new", Labels: []string{label}}}, Relationships: []contract.GraphRelationship{{ElementID: "edge", StartElementID: "new", EndElementID: "outside", Type: label}}}
			d.transfer.GraphBundle = &bundle
			d.transfer.GraphBindings = []contract.GraphImportBinding{{SourceID: "outside", DestinationID: anchor}}
			d.transfer.Step = 1
			if phase == "late-cancel" || phase == "late-close" {
				relay.armed.Store(true)
			}
			s.previewGraphTransfer(ui.Services{}, d)
			ticket, results := d.transfer.GraphTicket, d.transfer.Results
			if phase == "late-cancel" || phase == "late-close" {
				for _, ack := range []<-chan struct{}{relay.observed, relay.responded} {
					select {
					case <-ack:
					case <-time.After(15 * time.Second):
						t.Fatal("original graph anchor observation not withheld")
					}
				}
				if phase == "late-close" {
					s.Close(d)
				} else {
					s.stopDataTransfer(d)
					if !d.transfer.Busy || !d.transfer.Canceling {
						t.Fatal("preview cancel preceded original acknowledgement")
					}
					waitNeo4jData(t, s, d)
					if d.transfer.GraphPlan != nil || d.transfer.Uncertain || d.transfer.GraphBundle == nil {
						t.Fatal("canceled graph preview lost source or invented unknown write")
					}
				}
			} else {
				var ready mysqlDataTransferResult
				select {
				case ready = <-results:
				case <-time.After(15 * time.Second):
					t.Fatal("original graph preview missing")
				}
				buffer := make(chan mysqlDataTransferResult, 1)
				buffer <- ready
				d.transfer.Results = buffer
				switch phase {
				case "park":
					profile := active.ProfileID
					active.ProfileID = 999
					s.consumeDataTransfers(ui.Services{})
					if !d.transfer.Busy || d.transfer.GraphPlan != nil {
						t.Fatal("parked graph preview changed active state")
					}
					captureNeo4jData(t, s, d, "graph-import-preview-parked", func(c *ui.Context) { s.transferModals(c, d) })
					active.ProfileID = profile
					s.consumeDataTransfers(ui.Services{})
					if d.transfer.GraphPlan == nil || d.transfer.GraphSnapshot.NativeJSON == "" {
						t.Fatal("parked graph preview lost original handle")
					}
					s.stopDataTransfer(d)
				case "generation":
					replacement, err := manager.ConnectSession(context.Background(), neo4jDataProfile(t, f), f.Secret)
					if err != nil {
						t.Fatal(err)
					}
					active.Generation = replacement.Generation()
					s.consumeDataTransfers(ui.Services{})
					if d.transfer.GraphPlan != nil || d.transfer.GraphBundle == nil || d.transfer.Uncertain {
						t.Fatal("stale graph preview adopted replacement or discarded source")
					}
				case "unconsumed-close":
					s.Close(d)
				}
			}
			if phase == "late-close" {
				select {
				case <-results:
				case <-time.After(15 * time.Second):
					t.Fatal("closed original preview executor did not acknowledge")
				}
			}
			ticket.mu.Lock()
			remaining, revoked := ticket.plan, ticket.revoked
			ticket.mu.Unlock()
			if remaining != nil || !revoked {
				t.Fatal("late/unconsumed original graph plan retained after cancellation")
			}
			relay.armed.Store(false)
			relay.hold.Store(false)
			count := neo4jDataRun(t, admin, "MATCH (n) WHERE $label IN labels(n) RETURN count(n)", map[string]any{"label": label})
			if count.Rows[0][0].Text != "1" {
				t.Fatal("graph preview lifecycle wrote source entities")
			}
		})
	}
}
