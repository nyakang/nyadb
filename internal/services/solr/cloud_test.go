package solr

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestSolrCloudCaseSensitiveMembersAndChangedAliasCreationOrReload(t *testing.T) {
	for _, change := range []string{"none", "alias", "creation", "reload", "missing_member"} {
		t.Run(change, func(t *testing.T) {
			var schemaReads atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				changed := schemaReads.Load() > 0
				switch r.URL.Path {
				case "/solr/admin/info/system":
					fmt.Fprint(w, nodeJSON(100, true))
				case "/solr/admin/collections":
					if r.URL.Query().Get("action") == "LISTALIASES" {
						members := "First,Second"
						if changed && change == "alias" {
							members = "Second,First"
						}
						if change == "missing_member" {
							members = "First,missing"
						}
						fmt.Fprint(w, `{"responseHeader":{"status":0},"aliases":{"Alias":`+fmt.Sprintf("%q", members)+`,"alias":"First"},"properties":{"Alias":{"router.name":"time","router.field":"ts"}}}`)
					} else {
						fmt.Fprint(w, `{"responseHeader":{"status":0},"cluster":{"collections":{"First":{"configName":"owned","creationTimeMillis":100,"znodeVersion":0,"shards":{"s1":{"state":"active"}}},"Second":{"configName":"owned","creationTimeMillis":200,"znodeVersion":0,"shards":{"s1":{"state":"active"}}}}}}`)
					}
				case "/solr/admin/zookeeper":
					czxid := 100
					if changed && change == "creation" {
						czxid = 101
					}
					fmt.Fprintf(w, `{"znode":{"path":%q,"prop":{"ctime":"owned boot","czxid":%d}}}`, r.URL.Query().Get("path"), czxid)
				case "/solr/admin/cores":
					started := "2026-11-08T00:00:00Z"
					if changed && change == "reload" {
						started = "2026-11-08T00:00:01Z"
					}
					core := strings.TrimSuffix(mockCore("First_shard1", "/owned", started), "}") + `,"cloud":{"collection":"First"}}`
					fmt.Fprint(w, `{"responseHeader":{"status":0},"status":{"First_shard1":`+core+`}}`)
				default:
					if strings.HasSuffix(r.URL.Path, "/schema") {
						schemaReads.Add(1)
						fmt.Fprint(w, `{"responseHeader":{"status":0},"schema":`+testSchema+`}`)
					} else {
						fmt.Fprint(w, `{"responseHeader":{"status":0},"config":`+testConfig+`}`)
					}
				}
			}))
			defer server.Close()
			s := testSession(t, connections.SolrConfig{Endpoint: server.URL}, "")
			objects, err := s.Browse(context.Background(), s.Namespace(""))
			if change == "missing_member" {
				if !errors.Is(err, ErrProtocol) {
					t.Fatal("missing alias member accepted", err)
				}
				return
			}
			if err != nil || len(objects) != 4 {
				t.Fatal(objects, err)
			}
			m, err := s.TableMetadata(context.Background(), s.Namespace("").Object("alias", "Alias"))
			if change != "none" {
				if !errors.Is(err, ErrChanged) {
					t.Fatal("changed cloud target published", err)
				}
				return
			}
			if err != nil || len(m.Columns) != 6 || m.Columns[0].Name != "First:id" || m.Columns[3].Name != "Second:id" || len(m.Definitions) != 6 || !strings.Contains(m.Definition, "router.field") || m.Editable {
				t.Fatal("alias schema merged or resolution lost", m, err)
			}
			lower, err := s.TableMetadata(context.Background(), s.Namespace("").Object("alias", "alias"))
			if err != nil || len(lower.Columns) != 3 || lower.Columns[0].Name != "id" {
				t.Fatal("case-sensitive alias collapsed", lower, err)
			}
		})
	}
}
