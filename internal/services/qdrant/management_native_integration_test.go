package qdrant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"
)

func nativeManagement(t *testing.T, c *Client, p ManagementPlan) ManagementReceipt {
	t.Helper()
	r, err := c.SubmitManagement(context.Background(), p)
	if err != nil || !r.HasResult || !r.Result || r.Response.Status != 200 {
		t.Fatal("original native management boolean", err, string(r.Response.Body))
	}
	if _, err = c.SubmitManagement(context.Background(), p); !errors.Is(err, ErrManagementUsed) {
		t.Fatal("settled native management plan reused", err)
	}
	return r
}
func nativeCollectionPlan(t *testing.T, name, action, body string) ManagementPlan {
	t.Helper()
	p, err := PrepareCollectionManagement(name, action, json.RawMessage(body), 30)
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func nativeAliasPlan(t *testing.T, changes ...AliasChange) ManagementPlan {
	t.Helper()
	p, err := PrepareAliasManagement(changes, 30)
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func nativeManagementActionPlan(t *testing.T, name, action string) ManagementPlan {
	t.Helper()
	switch action {
	case "collection_create":
		return nativeCollectionPlan(t, name+"_created", action, `{"vectors":{"size":2,"distance":"Dot"}}`)
	case "collection_update":
		return nativeCollectionPlan(t, name, action, `{"params":{"on_disk_payload":true}}`)
	case "collection_delete":
		return nativeCollectionPlan(t, name, action, "")
	case "aliases":
		return nativeAliasPlan(t, AliasChange{Action: "create_alias", Alias: name + "_alias", Collection: name})
	}
	t.Fatal("unknown native management action")
	return ManagementPlan{}
}
func TestQdrantNativeManagementCollectionConfigurationExactReadbackAndDelete(t *testing.T) {
	c := nativeOpen(t)
	name := "owned_manage_" + fmt.Sprint(time.Now().UnixNano())
	body := `{"vectors":{"dense":{"size":2,"distance":"Cosine"},"multi":{"size":2,"distance":"Dot","multivector_config":{"comparator":"max_sim"}}},"sparse_vectors":{"sparse":{}},"on_disk_payload":false,"metadata":{"marker":"old","n":9007199254740993}}`
	nativeManagement(t, c, nativeCollectionPlan(t, name, "collection_create", body))
	info, _, err := c.Collection(context.Background(), name)
	if err != nil || !strings.Contains(string(info.Result), "9007199254740993") || !strings.Contains(string(info.Result), `"sparse"`) || !strings.Contains(string(info.Result), `"multi"`) {
		t.Fatal("exact native collection configuration", err, string(info.Result))
	}
	nativeUpsert(t, c, name, `{"points":[{"id":18446744073709551615,"vector":{"dense":[3,4],"multi":[[1,0]],"sparse":{"indices":[1],"values":[0.5]}},"payload":{"marker":"retained"}}]}`)
	nativeManagement(t, c, nativeCollectionPlan(t, name, "collection_update", `{"params":{"on_disk_payload":true},"hnsw_config":{"m":24},"metadata":{"marker":"next","n":9007199254740993}}`))
	info, _, err = c.Collection(context.Background(), name)
	fields, _ := object(info.Result)
	config, _ := object(fields["config"])
	params, _ := object(config["params"])
	hnsw, _ := object(config["hnsw_config"])
	if err != nil || string(params["on_disk_payload"]) != "true" || string(hnsw["m"]) != "24" || !strings.Contains(string(config["metadata"]), "next") || !strings.Contains(string(config["metadata"]), "9007199254740993") {
		t.Fatal("native configuration update readback", err, string(info.Result))
	}
	point, _, err := c.ReadPoint(context.Background(), name, []byte(`18446744073709551615`))
	if err != nil || !strings.Contains(string(point.Payload), "retained") {
		t.Fatal("configuration update replaced existing points", err)
	}
	nativeManagement(t, c, nativeCollectionPlan(t, name, "collection_delete", ""))
	names, _, err := c.Collections(context.Background())
	if err != nil || slices.Contains(names, name) {
		t.Fatal("native collection deletion not observed", err)
	}
	// Native deletion of an already absent collection has a settled false result.
	r, err := c.SubmitManagement(context.Background(), nativeCollectionPlan(t, name, "collection_delete", ""))
	if err != nil || !r.HasResult || r.Result {
		t.Fatal("native absent deletion semantics", err, string(r.Response.Body))
	}
}

func TestQdrantNativeManagementAliasRetargetRenameAndFailedBatchEvidence(t *testing.T) {
	c := nativeOpen(t)
	a := nativeCollection(t, c, `{"vectors":{"size":2,"distance":"Dot"}}`)
	b := nativeCollection(t, c, `{"vectors":{"size":2,"distance":"Dot"}}`)
	alias, renamed := a+"_alias", a+"_renamed"
	nativeUpsert(t, c, a, `{"points":[{"id":1,"vector":[1,0],"payload":{"marker":"a"}}]}`)
	nativeUpsert(t, c, b, `{"points":[{"id":1,"vector":[0,1],"payload":{"marker":"b"}}]}`)
	nativeManagement(t, c, nativeAliasPlan(t, AliasChange{Action: "create_alias", Alias: alias, Collection: a}))
	nativeManagement(t, c, nativeAliasPlan(t, AliasChange{Action: "delete_alias", Alias: alias}, AliasChange{Action: "create_alias", Alias: alias, Collection: b}))
	point, _, err := c.ReadPoint(context.Background(), alias, []byte(`1`))
	if err != nil || !strings.Contains(string(point.Payload), `"b"`) {
		t.Fatal("native retarget did not route to original destination", err)
	}
	nativeManagement(t, c, nativeAliasPlan(t, AliasChange{Action: "rename_alias", Alias: alias, Destination: renamed}))
	aliases, _, err := c.Aliases(context.Background())
	if err != nil || !slices.Contains(aliases, Alias{Alias: renamed, Collection: b}) || slices.ContainsFunc(aliases, func(a Alias) bool { return a.Alias == alias }) {
		t.Fatal("native alias rename readback", err, aliases)
	}
	nativeManagement(t, c, nativeAliasPlan(t, AliasChange{Action: "delete_alias", Alias: renamed}))
	failedAlias := a + "_failed"
	p := nativeAliasPlan(t, AliasChange{Action: "create_alias", Alias: failedAlias, Collection: a}, AliasChange{Action: "create_alias", Alias: a + "_missing_alias", Collection: a + "_missing"})
	r, err := c.SubmitManagement(context.Background(), p)
	var interrupted *Interrupted
	if !errors.As(err, &interrupted) || !interrupted.WriteOutcomeUnknown || r.HasResult || len(r.Response.Body) == 0 {
		t.Fatal("failed alias batch lost original receipt", err)
	}
	reader := nativeOpen(t)
	aliases, _, err = reader.Aliases(context.Background())
	if err != nil || slices.ContainsFunc(aliases, func(a Alias) bool { return a.Alias == failedAlias }) {
		t.Fatal("native failed alias batch was partially applied", err, aliases)
	}
	if _, err = c.SubmitManagement(context.Background(), p); !errors.Is(err, ErrManagementUsed) {
		t.Fatal("failed original alias batch replayed", err)
	}
	t.Log("Native ordered retarget/rename/delete and actual failed alias-batch rollback observed; metadata observations are not remote CAS")
}

func TestQdrantNativeManagementPermissionsAndNativeSchemaFailure(t *testing.T) {
	c := nativeOpen(t)
	name := nativeCollection(t, c, `{"vectors":{"size":2,"distance":"Dot"}}`)
	p := nativeCollectionPlan(t, name, "collection_update", `{"params":{"replication_factor":0}}`)
	r, err := c.SubmitManagement(context.Background(), p)
	var interrupted *Interrupted
	if !errors.As(err, &interrupted) || !interrupted.WriteOutcomeUnknown || r.HasResult || len(r.Response.Body) == 0 {
		t.Fatal("native nested schema rejection became applied", err, string(r.Response.Body))
	}
	cfg, _, readonly := nativeFixture(t)
	if cfg.Auth == "api_key" {
		reader, err := Open(context.Background(), cfg, readonly)
		if err != nil {
			t.Fatal(err)
		}
		defer reader.Close()
		for _, action := range []string{"collection_update", "collection_create", "collection_delete", "aliases"} {
			// A failure retires its exact original executor; a new one is explicit.
			if action != "collection_update" {
				reader, err = Open(context.Background(), cfg, readonly)
				if err != nil {
					t.Fatal(err)
				}
				defer reader.Close()
			}
			p := nativeManagementActionPlan(t, name, action)
			r, err := reader.SubmitManagement(context.Background(), p)
			if err == nil || r.Response.Status != 403 || r.HasResult || len(r.Response.Body) == 0 {
				t.Fatal("native read-only management denial lost", action, err)
			}
		}
		observer := nativeOpen(t)
		names, _, err := observer.Collections(context.Background())
		aliases, _, aliasErr := observer.Aliases(context.Background())
		if err != nil || aliasErr != nil || !slices.Contains(names, name) || slices.Contains(names, name+"_created") || slices.ContainsFunc(aliases, func(a Alias) bool { return a.Alias == name+"_alias" }) {
			t.Fatal("denied native management changed collection or alias state", err, aliasErr)
		}
		info, _, err := observer.Collection(context.Background(), name)
		if err != nil || !strings.Contains(string(info.Result), `"replication_factor":1`) {
			t.Fatal("invalid native nested config changed existing state", err)
		}
	}
}

func TestQdrantNativeManagementLostCanceledAndOriginalCloseNeverReplay(t *testing.T) {
	for _, action := range []string{"collection_create", "collection_update", "collection_delete", "aliases"} {
		for _, fault := range []string{"lost", "canceled", "close"} {
			t.Run(action+"/"+fault, func(t *testing.T) {
				upstream := nativeOpen(t)
				name := nativeCollection(t, upstream, `{"vectors":{"size":2,"distance":"Dot"}}`)
				p := nativeManagementActionPlan(t, name, action)
				observed, acked := make(chan struct{}), make(chan struct{})
				cfg, calls := nativeFaultProxy(t, upstream, func(r *http.Request) bool {
					return r.Method == p.Preview().Method && r.URL.RequestURI() == p.Preview().Path
				}, func(w http.ResponseWriter, r *http.Request, response Response) {
					close(observed)
					if fault == "lost" {
						conn, _, e := w.(http.Hijacker).Hijack()
						if e != nil {
							t.Error(e)
						} else {
							conn.Close()
						}
					} else {
						<-r.Context().Done()
					}
					close(acked)
				})
				_, secret, _ := nativeFixture(t)
				c, err := Open(context.Background(), cfg, secret)
				if err != nil {
					t.Fatal(err)
				}
				defer c.Close()
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				out := make(chan error, 1)
				go func() { _, e := c.SubmitManagement(ctx, p); out <- e }()
				waitSessionSignal(t, observed)
				if fault == "canceled" {
					cancel()
				}
				if fault == "close" {
					c.Close()
				}
				select {
				case err = <-out:
				case <-time.After(10 * time.Second):
					t.Fatal("original management executor did not settle")
				}
				waitSessionSignal(t, acked)
				var interrupted *Interrupted
				if !errors.As(err, &interrupted) || !interrupted.WriteOutcomeUnknown {
					t.Fatal("lost native management response became success", err)
				}
				fresh := nativeOpen(t)
				if _, err = fresh.SubmitManagement(context.Background(), p); !errors.Is(err, ErrManagementUsed) || calls.Load() != 1 {
					t.Fatal("original source replayed on recovery", err)
				}
				names, _, err := fresh.Collections(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				if action == "collection_create" && !slices.Contains(names, name+"_created") || action == "collection_delete" && slices.Contains(names, name) {
					t.Fatal("actual original native mutation was not observed")
				}
				if action == "collection_update" {
					info, _, err := fresh.Collection(context.Background(), name)
					if err != nil || !strings.Contains(string(info.Result), `"on_disk_payload":true`) {
						t.Fatal("actual native configuration update not observed", err)
					}
				}
				if action == "aliases" {
					aliases, _, err := fresh.Aliases(context.Background())
					if err != nil || !slices.Contains(aliases, Alias{Alias: name + "_alias", Collection: name}) {
						t.Fatal("actual native alias mutation not observed", err)
					}
				}
			})
		}
	}
}
