package sqlworkspace

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/dbruntime"
	"github.com/nyakang/nyadb/internal/services/neo4j"
)

func TestNeo4jNativePublicGraphPropertyExtendedLostCanceledOriginalCloseAndRecoveryNeverReplay(t *testing.T) {
	testPublicExtendedPropertyFaults(t, false)
}

func TestNeo4jNativePublicGraphPropertyExtendedListsLostCanceledOriginalCloseAndRecoveryNeverReplay(t *testing.T) {
	testPublicExtendedPropertyFaults(t, true)
}

func testPublicExtendedPropertyFaults(t *testing.T, lists bool) {
	samples := extendedNativeSamples()
	if lists {
		samples = extendedListNativeSamples()
	}
	f := neo4jWorkbenchFixtureTest(t)
	admin, err := neo4j.Open(context.Background(), f.Config, f.Secret)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	label := "NyaExtendedFault_" + f.Nonce
	if lists {
		label += "_lists"
	}
	defer neo4jNativeRun(t, admin, contract.GraphQueryRequest{Query: "MATCH (n) WHERE $label IN labels(n) DETACH DELETE n", Parameters: map[string]any{"label": label}})
	for _, sample := range samples {
		for _, operation := range []string{"edit", "add", "remove"} {
			for _, fault := range []string{"lost", "cancel", "close"} {
				t.Run(sample.kind+"/"+operation+"/"+fault, func(t *testing.T) {
					first, after := extendedNativeValue(t, sample.kind, sample.first), extendedNativeValue(t, sample.kind, sample.after)
					property := "typed ` 猫 property"
					query := "CREATE (n:$($label) {guard:'unchanged'})"
					if operation != "add" {
						query += " SET n[$key]=$value"
					}
					created := neo4jNativeRun(t, admin, contract.GraphQueryRequest{Query: query + " RETURN n", Parameters: map[string]any{"label": label, "key": property, "value": extendedNativeParameter(t, first)}})
					root := created.Rows[0][0].Node.ElementID
					relay := newNeo4jRelay(t, f, "SET e[$key]=e[$key]")
					originalFixture := f
					originalFixture.Config.URI = "bolt://" + relay.listener.Addr().String()
					originalFixture.Config.CAFile, originalFixture.Config.ClientCertFile, originalFixture.Config.ClientKeyFile = "", "", ""
					p := neo4jWorkbenchProfile(t, originalFixture)
					m := dbruntime.New()
					dbruntime.RegisterBuiltins(m, nil)
					defer m.Close()
					if _, err := m.ConnectSession(context.Background(), p, f.Secret); err != nil {
						t.Fatal(err)
					}
					runtime := EngineRuntime(m.Session)
					s := New(Dependencies{QueryDialect: func(int64) string { return "neo4j" }, Namespace: m.Namespace, Runtime: func() Runtime { return runtime }, Connected: m.IsConnected})
					defer s.CloseAll()
					source := "MATCH (n) WHERE elementId(n)=$id RETURN n"
					d := s.Open(1, source, p.ID, "neo4j", false)
					d.layoutMode = "result"
					parameters, _ := json.Marshal(map[string]any{"id": root})
					d.graphParameters = string(parameters)
					s.Prepare(d)
					waitNativeNeighbors(t, s, d)
					s.Run(nil, d)
					waitNativeNeighbors(t, s, d)
					if d.Error() != "" {
						t.Fatal(d.Error())
					}
					d.graphVisualOpen = true
					original, _ := json.Marshal(d.result)
					g := d.graphVisualState()
					g.focus("n:" + root)
					if operation == "add" {
						s.openGraphPropertyCreation(d, g)
						d.graphPropertyDraft.name, d.graphPropertyDraft.kind, d.graphPropertyDraft.text = property, sample.kind, sample.after
					} else {
						for _, value := range g.properties() {
							if value.Name == property {
								if operation == "remove" {
									s.openGraphPropertyRemoval(d, g, value)
								} else {
									s.openGraphPropertyDraft(d, g, value)
									d.graphPropertyDraft.text = sample.after
								}
								break
							}
						}
					}
					s.previewGraphProperty(d)
					if d.graphPropertyDraft == nil || d.graphPropertyDraft.preview == nil {
						t.Fatal("original extended fault request unavailable")
					}
					committed := after
					if operation == "remove" {
						committed = contract.GraphValue{Kind: "null"}
					}
					s.saveGraphProperty(d)
					select {
					case <-relay.observed:
					case <-time.After(15 * time.Second):
						t.Fatal("original extended RUN was not forwarded")
					}
					select {
					case <-relay.responded:
					case <-time.After(15 * time.Second):
						t.Fatal("native server did not return original extended write")
					}
					observed := false
					deadline := time.Now().Add(10 * time.Second)
					for time.Now().Before(deadline) {
						read := neo4jNativeRun(t, admin, contract.GraphQueryRequest{Query: "MATCH (n) WHERE elementId(n)=$id RETURN n[$key]", Mode: "read", Parameters: map[string]any{"id": root, "key": property}})
						if reflect.DeepEqual(read.Rows[0][0], committed) {
							observed = true
							break
						}
						time.Sleep(time.Millisecond)
					}
					if !observed {
						t.Fatal("native extended commit was not independently observed before fault")
					}
					switch fault {
					case "lost":
						relay.Close()
					case "cancel":
						s.RequestCancel(d)
						if !d.Loading() || !d.CancelRequested() {
							t.Fatal("extended cancellation settled without executor acknowledgement")
						}
					case "close":
						s.Close(d)
					}
					if fault != "close" {
						waitNativeNeighbors(t, s, d)
						current, _ := json.Marshal(d.result)
						if !d.NeedsRecovery() || !bytes.Equal(current, original) || d.graphPropertyDraft == nil || !d.graphPropertyDraft.unresolved || len(d.graphPropertyRecords) != 1 || d.graphPropertyRecords[0].Receipt.Applied || !d.graphPropertyRecords[0].Receipt.Result.Uncertain || d.graphPropertyRecords[0].Receipt.Result.Completed {
							t.Fatal("missing extended completion lost evidence or fabricated rollback", d.Error())
						}
						s.saveGraphProperty(d)
						s.Run(nil, d)
						if d.Loading() {
							t.Fatal("uncertain extended operation replayed")
						}
					}
					// A distinctive typed value makes any original replay observable.
					neo4jNativeRun(t, admin, contract.GraphQueryRequest{Query: "MATCH (n) WHERE elementId(n)=$id SET n[$key]=$value", Parameters: map[string]any{"id": root, "key": property, "value": extendedNativeParameter(t, first)}})
					freshProfile := neo4jWorkbenchProfile(t, f)
					fresh, err := m.ConnectSession(context.Background(), freshProfile, f.Secret)
					if err != nil {
						t.Fatal(err)
					}
					if fault != "close" {
						s.Recover(d)
						waitNativeNeighbors(t, s, d)
						if d.result != nil || d.graphVisual != nil || d.graphPropertyDraft == nil || len(d.graphPropertyRecords) != 1 || d.Editor.Buffer.String() != source {
							t.Fatal("extended recovery discarded evidence or replayed Cypher")
						}
						s.saveGraphProperty(d)
						if d.Loading() {
							t.Fatal("recovered unresolved extended draft was replayed")
						}
						s.verifyGraphProperty(d, 0)
						waitNativeNeighbors(t, s, d)
						if len(d.graphPropertyRecords) != 2 || !d.graphPropertyRecords[1].Verification || d.graphPropertyRecords[1].Receipt.Applied || !d.graphPropertyRecords[0].Receipt.Result.Uncertain || !d.graphPropertyDraft.unresolved {
							t.Fatal("extended readback supplied missing write completion")
						}
						s.Close(d)
					}
					read := neo4jNativeRun(t, admin, contract.GraphQueryRequest{Query: "MATCH (n) WHERE elementId(n)=$id RETURN n[$key],n.guard", Mode: "read", Parameters: map[string]any{"id": root, "key": property}})
					if !reflect.DeepEqual(read.Rows[0][0], first) || read.Rows[0][1].Text != "unchanged" || relay.dispatches.Load() != 1 || !m.IsConnected(p.ID) || m.Generation(p.ID) != fresh.Generation() {
						t.Fatal("original extended recovery/close replayed, lost components or closed replacement")
					}
				})
			}
		}
	}
}
