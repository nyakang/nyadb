package dataworkspace

import (
	"context"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/neo4j"
)

func TestNeo4jNativePublicDataRelationshipEndpointReadPreviewParkGenerationCancelAndClose(t *testing.T) {
	f := neo4jDataFixtureTest(t)
	admin, err := neo4j.Open(context.Background(), f.Config, f.Secret)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	for _, operation := range []string{"read", "preview"} {
		for _, phase := range []string{"park", "generation", "unconsumed-close", "late-cancel", "late-close"} {
			t.Run(operation+"/"+phase, func(t *testing.T) {
				label, rel := "NyaPublicRelLife_"+f.Nonce+"_"+operation+"_"+phase, "NyaPublicRelLifeType_"+f.Nonce+"_"+operation+"_"+phase
				seed := neo4jDataRun(t, admin, "CREATE (a:$($label) {guard:1}),(b:$($label) {guard:2}) CREATE (a)-[:$($rel)]->(b) RETURN a,b", map[string]any{"label": label, "rel": rel})
				defer neo4jDataRun(t, admin, "MATCH (n) WHERE $label IN labels(n) DETACH DELETE n", map[string]any{"label": label})
				source, target := seed.Rows[0][0].Node.ElementID, seed.Rows[0][1].Node.ElementID
				relay := newNeo4jDataRelay(t, f, "RETURN source,target LIMIT 2")
				original := f
				original.Config.URI = "bolt://" + relay.listener.Addr().String()
				original.Config.CAFile, original.Config.ClientCertFile, original.Config.ClientKeyFile = "", "", ""
				_, manager, s, active, caps := openNeo4jEntityWorkbench(t, original)
				object := manager.Namespace(active.ProfileID, "neo4j").Object("relationship", rel)
				d := s.OpenObject(1, object, caps)
				s.Load(ui.Services{}, d, false)
				waitNeo4jData(t, s, d)
				if operation == "preview" {
					publicRelationshipDraft(t, s, d, source, target)
				} else {
					entityTestClick(t, s, d, "添加记录")
					d.graphEndpoints.source, d.graphEndpoints.target = source, target
				}
				if phase == "late-cancel" || phase == "late-close" {
					relay.armed.Store(true)
				}
				if operation == "preview" {
					s.Save(ui.Services{}, d)
				} else {
					s.readDataGraphEndpoints(ui.Services{}, d)
				}
				var endpointResults <-chan dataGraphEndpointResult
				var previewResults <-chan error
				var ticket *dataMutationTicket
				if operation == "read" {
					endpointResults = d.graphEndpoints.results
				} else {
					previewResults, ticket = d.mutation.results, d.mutation.ticket
				}
				if phase == "late-cancel" || phase == "late-close" {
					for _, ack := range []<-chan struct{}{relay.observed, relay.responded} {
						select {
						case <-ack:
						case <-time.After(15 * time.Second):
							t.Fatal("native original endpoint read not withheld")
						}
					}
					if phase == "late-close" {
						s.Close(d)
					} else {
						s.stopMySQLDataEdit(d)
						if !d.edit.Busy {
							t.Fatal("endpoint cancellation preceded native acknowledgement")
						}
						if operation == "read" {
							waitEndpointRead(t, s, d)
						} else {
							deadline := time.Now().Add(30 * time.Second)
							for d.mutation.results != nil && time.Now().Before(deadline) {
								s.Consume(ui.Services{})
								time.Sleep(time.Millisecond)
							}
							if d.mutation.results != nil {
								t.Fatal("canceled endpoint preview did not settle")
							}
						}
						if d.edit.Busy || d.edit.Uncertain != nil || d.graphEndpoints != nil || operation == "preview" && !d.HasDraft() {
							t.Fatal("endpoint cancellation retained authorization or lost drafts")
						}
					}
					if phase == "late-close" {
						if operation == "read" {
							select {
							case r := <-endpointResults:
								if r.endpoints != nil || r.err == nil {
									t.Fatal("closed native endpoint read returned authorization")
								}
							case <-time.After(15 * time.Second):
								t.Fatal("closed read did not acknowledge")
							}
						} else {
							select {
							case <-previewResults:
							case <-time.After(15 * time.Second):
								t.Fatal("closed endpoint preview did not acknowledge")
							}
						}
					}
					if ticket != nil && ticket.take() != nil {
						t.Fatal("canceled/closed preview retained native handle")
					}
				} else {
					var plan contract.DataMutationPlan
					if operation == "read" {
						r := awaitPublicEndpointResult(t, d)
						if r.err != nil || r.endpoints == nil {
							t.Fatal("original endpoint observation did not complete", r.err)
						}
					} else {
						select {
						case e := <-previewResults:
							if e != nil {
								t.Fatal(e)
							}
							out := make(chan error, 1)
							out <- e
							d.mutation.results = out
						case <-time.After(30 * time.Second):
							t.Fatal("native endpoint preview stalled")
						}
						ticket.mu.Lock()
						plan = ticket.plan
						ticket.mu.Unlock()
						if plan == nil {
							t.Fatal("native endpoint preview missing")
						}
					}
					if phase == "unconsumed-close" {
						s.Close(d)
						if d.graphEndpoints != nil || d.mutation.snapshot.NativeJSON != "" || ticket != nil && ticket.take() != nil || plan != nil && plan.Snapshot().Object != (contract.ObjectRef{}) {
							t.Fatal("unconsumed original close retained observations/plan")
						}
					} else {
						profile := active.ProfileID
						active.ProfileID = 99
						s.Consume(ui.Services{})
						if !d.edit.Busy || operation == "read" && d.graphEndpoints.observation != nil || operation == "preview" && d.mutation.open {
							t.Fatal("another profile consumed original read/preview")
						}
						if phase == "generation" {
							replacement, e := manager.ConnectSession(context.Background(), neo4jDataProfile(t, f), f.Secret)
							if e != nil {
								t.Fatal(e)
							}
							active.ProfileID = profile
							s.Consume(ui.Services{})
							if d.edit.Busy || d.edit.Uncertain != nil || d.graphEndpoints != nil || operation == "preview" && (!d.HasDraft() || plan.Snapshot().Object != (contract.ObjectRef{})) {
								t.Fatal("old endpoint result authorized replacement generation")
							}
							s.Close(d)
							if manager.Generation(profile) != replacement.Generation() || !manager.IsConnected(profile) {
								t.Fatal("old endpoint close damaged replacement session")
							}
						} else {
							active.ProfileID = profile
							s.Consume(ui.Services{})
							if operation == "read" {
								if d.graphEndpoints == nil || d.graphEndpoints.observation == nil {
									t.Fatal("original parked endpoints not admitted on return")
								}
								captureNeo4jData(t, s, d, "relationship-endpoint-parked", func(c *ui.Context) { s.View(c, d); s.Modals(c) })
								entityTestClick(t, s, d, "取消")
								if d.edit.Busy || d.HasDraft() {
									t.Fatal("canceling returned endpoint choice created a draft")
								}
							} else {
								waitNeo4jMutationPreview(t, s, d)
								entityTestClick(t, s, d, "取消")
								if !d.HasDraft() || d.edit.Busy {
									t.Fatal("canceling returned endpoint preview lost drafts")
								}
							}
						}
					}
				}
				relay.armed.Store(false)
				relay.hold.Store(false)
				observed := neo4jDataRun(t, admin, "MATCH ()-[r]->() WHERE type(r)=$rel RETURN count(r)", map[string]any{"rel": rel})
				if observed.Rows[0][0].Text != "1" {
					t.Fatal("original endpoint lifecycle wrote relationships")
				}
				s.CloseAll()
				manager.Close()
			})
		}
	}
}
