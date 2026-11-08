package qdrant

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func pointCells(t *testing.T, set contract.ResultSet, row int) map[string]contract.Cell {
	t.Helper()
	if row >= len(set.Rows) || len(set.Rows[row]) != len(set.Columns) {
		t.Fatal("missing native point row")
	}
	out := map[string]contract.Cell{}
	for i, col := range set.Columns {
		out[col.Name] = set.Rows[row][i]
	}
	return out
}

func TestQdrantPointQueriesPreserveTypedScalarsVectorsAndOriginalEnvelope(t *testing.T) {
	point := `{"id":18446744073709551615,"score":1.2300e-20,"version":9007199254740993,"vector":{"dense":[0.25,0.5],"sparse":{"indices":[2,8],"values":[0.25,0.5]},"multi":[[1,0],[0,1]]},"payload":{"integer":9007199254740995,"decimal":-1.2300e-20,"text":"原生🐱","empty":"","flag":true,"nil":null,"nested":{"n":9007199254740993}}}`
	for _, tc := range []struct{ request, result string }{
		{"POST /collections/owned/points/scroll?timeout=3\n{}", `{"points":[` + point + `],"next_page_offset":18446744073709551615}`},
		{"POST /collections/owned/points/query\n{}", `{"points":[` + point + `]}`},
		{"POST /collections/owned/points\n{\"ids\":[18446744073709551615]}", `[` + point + `]`},
		{"POST /collections/owned/points/search\n{}", `[` + point + `]`},
		{"POST /collections/owned/points/recommend\n{}", `[` + point + `]`},
		{"POST /collections/owned/points/discover\n{}", `[` + point + `]`},
		{"GET /collections/owned/points/18446744073709551615", point},
	} {
		t.Run(strings.Fields(tc.request)[1], func(t *testing.T) {
			raw := `{"status":"ok","time":1.2300e-20,"future":{"n":9007199254740993},"result":` + tc.result + `}`
			var calls atomic.Int32
			server := sessionServer(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1); fmt.Fprint(w, raw) })
			s := testSession(t, connections.QdrantConfig{Endpoint: server.URL}, "")
			l, _ := s.ReserveQuery(1)
			result, err := l.Execute(context.Background(), "owned", tc.request)
			if err != nil || len(result.Sets) != 2 || result.Steps[0].State != "completed" || result.AffectedRows != 0 || result.Truncated || calls.Load() != 1 {
				t.Fatal("native query result/outcome", result, err)
			}
			cells := pointCells(t, result.Sets[0], 0)
			for name, want := range map[string]contract.Cell{
				"id":      {Kind: contract.Integer, Text: "18446744073709551615"},
				"score":   {Kind: contract.Decimal, Text: "1.2300e-20"},
				"version": {Kind: contract.Integer, Text: "9007199254740993"},
				"integer": {Kind: contract.Integer, Text: "9007199254740995"},
				"decimal": {Kind: contract.Decimal, Text: "-1.2300e-20"},
				"text":    {Kind: contract.Text, Text: "原生🐱"},
				"empty":   {Kind: contract.Text},
				"flag":    {Kind: contract.Boolean, Text: "true"},
				"nil":     {Kind: contract.Null},
				"nested":  {Kind: contract.JSON, Text: `{"n":9007199254740993}`},
			} {
				if cells[name].Kind != want.Kind || cells[name].Text != want.Text {
					t.Fatal("native scalar changed", name, cells[name], want)
				}
			}
			if _, ok := cells["payload"]; ok || cells["vector"].Kind != contract.JSON || !strings.Contains(cells["vector"].Text, `"sparse"`) || result.Columns[0] != "id" || result.Rows[0][0] != "18446744073709551615" || result.Sets[1].Rows[0][1].Text != raw {
				t.Fatal("projection lost vector, legacy rows or original envelope")
			}
		})
	}
}

func TestQdrantPointPayloadCollisionAcrossRowsKeepsEveryOriginalWrapper(t *testing.T) {
	raw := `{"status":"ok","result":{"points":[{"id":1,"payload":{"score":"business","x":2}},{"id":"550e8400-e29b-41d4-a716-446655440000","score":0.5,"payload":{"x":3}}]}}`
	server := sessionServer(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, raw) })
	s := testSession(t, connections.QdrantConfig{Endpoint: server.URL}, "")
	l, _ := s.ReserveQuery(1)
	result, err := l.Execute(context.Background(), "owned", "POST /collections/owned/points/query\n{}")
	if err != nil || len(result.Sets) != 2 {
		t.Fatal("native collision result", err)
	}
	a, b := pointCells(t, result.Sets[0], 0), pointCells(t, result.Sets[0], 1)
	if a["score"].Kind != contract.Null || b["score"].Text != "0.5" || a["payload"].Text != `{"score":"business","x":2}` || b["payload"].Text != `{"x":3}` || b["id"].Kind != contract.Text || result.Sets[1].Rows[0][1].Text != raw {
		t.Fatal("business score mixed with native score", a, b)
	}
	if _, ok := a["x"]; ok {
		t.Fatal("inconsistent wrapper projection")
	}
}

func TestQdrantPointEmptyCountAndUnknownResponsesDoNotInventPointRows(t *testing.T) {
	for _, tc := range []struct {
		request, raw string
		sets, rows   int
	}{
		{"POST /collections/owned/points/scroll\n{}", `{"status":"ok","result":{"points":[],"next_page_offset":null}}`, 2, 0},
		{"POST /collections/owned/points/count\n{}", `{"status":"ok","result":{"count":18446744073709551615}}`, 2, 1},
		{"GET /collections/owned", `{"status":"ok","result":{"collections":[{"id":1,"payload":{"x":2}}]}}`, 1, 1},
		{"POST /collections/aliases\n{}", `{"status":"ok","result":[{"id":1,"payload":{"x":2}}]}`, 1, 1},
		{"PUT /collections/owned/points\n{}", `{"status":"ok","result":{"operation_id":1,"status":"completed","points":[{"id":1,"payload":{"x":2}}]}}`, 1, 1},
		{"POST /collections/owned/points/query/batch\n{}", `{"status":"ok","result":[{"points":[{"id":1,"payload":{"x":2}}]}]}`, 1, 1},
		{"POST /collections/owned/points/query/\n{}", `{"status":"ok","result":{"points":[{"id":1,"payload":{"x":2}}]}}`, 1, 1},
		{"POST /collections/owned%2Fpoints/query\n{}", `{"status":"ok","result":{"points":[{"id":1,"payload":{"x":2}}]}}`, 1, 1},
	} {
		t.Run(tc.request, func(t *testing.T) {
			server := sessionServer(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, tc.raw) })
			s := testSession(t, connections.QdrantConfig{Endpoint: server.URL}, "")
			l, _ := s.ReserveQuery(1)
			result, err := l.Execute(context.Background(), "", tc.request)
			if err != nil || len(result.Sets) != tc.sets || len(result.Sets[0].Rows) != tc.rows || result.AffectedRows != 0 || result.Sets[tc.sets-1].Rows[0][1].Text != tc.raw {
				t.Fatal("native envelope misclassified", result, err)
			}
			if strings.Contains(tc.request, "/count") && result.Sets[0].Rows[0][0].Text != "18446744073709551615" {
				t.Fatal("native count precision lost")
			}
		})
	}
}

func TestQdrantPointInvalidAndWideResponsesKeepBodyAndAllowNextOriginalRead(t *testing.T) {
	wide := `{"status":"ok","result":{"points":[{"id":1,"payload":{`
	for i := 0; i < pointColumnLimit; i++ {
		if i != 0 {
			wide += ","
		}
		wide += fmt.Sprintf(`"field_%d":%d`, i, i)
	}
	wide += `}}]}}`
	for _, raw := range []string{
		`{"status":"ok","result":{"points":[{"id":18446744073709551616}]}}`,
		`{"status":"ok","result":{"points":[{"id":1},{"id":1}]}}`,
		`{"status":"ok","result":{"points":[{"id":1,"payload":[]}]}}`,
		`{"status":"ok","result":{"points":[{"id":1,"payload":{"x":1,"x":2}}]}}`,
		`{"status":"ok","result":[{"id":1}]}`,
		wide,
	} {
		server := sessionServer(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, raw) })
		s := testSession(t, connections.QdrantConfig{Endpoint: server.URL}, "")
		l, _ := s.ReserveQuery(1)
		result, err := l.Execute(context.Background(), "", "POST /collections/owned/points/query\n{}")
		if err == nil || errors.Is(err, contract.ErrSQLSessionLost) || len(result.Sets) != 1 || result.Sets[0].Rows[0][1].Text != raw || result.Truncated || result.Steps[0].State != "failed" {
			t.Fatal("invalid native read discarded evidence or implied uncertain write", err)
		}
		if raw == wide && !errors.Is(err, ErrBounds) {
			t.Fatal("column bound not explicit", err)
		}
		if _, err := l.Execute(context.Background(), "", "GET /"); err != nil {
			t.Fatal("projection failure retired original lease", err)
		}
	}
}

func TestQdrantPointSinkFailureKeepsRowsAndOriginalResponseWithoutReplay(t *testing.T) {
	raw := `{"status":"ok","result":{"points":[{"id":1,"payload":{"text":"original"}},{"id":2,"payload":{"text":"second"}}]}}`
	var calls atomic.Int32
	server := sessionServer(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1); fmt.Fprint(w, raw) })
	s := testSession(t, connections.QdrantConfig{Endpoint: server.URL}, "")
	l, _ := s.ReserveQuery(1)
	result, err := l.ExecuteStream(context.Background(), "", "POST /collections/owned/points/query\n{}", func(index int, cols []contract.Column, cells []contract.Cell) error {
		cols[0].Name, cells[0].Text = "changed", "changed"
		return errors.New("sink failed")
	})
	if err == nil || len(result.Sets) != 2 || result.Steps[0].State != "completed" || len(result.Sets[0].Rows) != 2 || result.Sets[0].Columns[0].Name != "id" || result.Sets[0].Rows[0][0].Text != "1" || result.Sets[1].Rows[0][1].Text != raw || calls.Load() != 1 || result.Truncated {
		t.Fatal("sink changed settled native result", result, err)
	}
}

func TestQdrantPointServicePrefixAndEncodedCollectionPreserveOriginalRequest(t *testing.T) {
	raw := `{"status":"ok","result":{"points":[{"id":1,"payload":{"text":"prefixed"}}]}}`
	var path string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/gateway/qdrant/":
			fmt.Fprint(w, unitIdentity)
		case "/gateway/qdrant/collections":
			fmt.Fprint(w, sessionCollections)
		default:
			if r.Method != "POST" {
				t.Error("native method changed")
			}
			path = r.URL.EscapedPath()
			fmt.Fprint(w, raw)
		}
	}))
	defer server.Close()
	s := testSession(t, connections.QdrantConfig{Endpoint: server.URL + "/gateway/qdrant"}, "")
	l, _ := s.ReserveQuery(1)
	result, err := l.Execute(context.Background(), "owned", "POST /collections/%E7%8C%AB/points/query\n{}")
	if err != nil || len(result.Sets) != 2 || pointCells(t, result.Sets[0], 0)["text"].Text != "prefixed" || path != "/gateway/qdrant/collections/%E7%8C%AB/points/query" || result.Sets[1].Rows[0][1].Text != raw {
		t.Fatal("original endpoint prefix/encoded collection changed", path, err)
	}
}

func TestQdrantPointRowsAndCountBoundsKeepCompleteNativeEvidence(t *testing.T) {
	var rows strings.Builder
	rows.WriteString(`{"status":"ok","result":{"points":[`)
	for i := 0; i <= pointResultLimit; i++ {
		if i != 0 {
			rows.WriteByte(',')
		}
		fmt.Fprintf(&rows, `{"id":%d}`, i)
	}
	rows.WriteString(`]}}`)
	for _, tc := range []struct{ source, raw string }{
		{"POST /collections/owned/points/query\n{}", rows.String()},
		{"POST /collections/owned/points/count\n{}", `{"status":"ok","result":{"count":18446744073709551616}}`},
		{"POST /collections/owned/points/count\n{}", `{"status":"ok","result":{"count":1.0}}`},
	} {
		server := sessionServer(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, tc.raw) })
		s := testSession(t, connections.QdrantConfig{Endpoint: server.URL}, "")
		l, _ := s.ReserveQuery(1)
		result, err := l.Execute(context.Background(), "", tc.source)
		if err == nil || errors.Is(err, contract.ErrSQLSessionLost) || result.Truncated || len(result.Sets) != 1 || result.Sets[0].Rows[0][1].Text != tc.raw {
			t.Fatal("bounded projection discarded original evidence", err)
		}
		if strings.Contains(tc.source, "/query") && !errors.Is(err, ErrBounds) {
			t.Fatal("row limit not explicit", err)
		}
		if _, err := l.Execute(context.Background(), "", "GET /"); err != nil {
			t.Fatal("projection limit retired original lease", err)
		}
	}
}

func TestQdrantPointPostCancellationClassifiesReadWithoutReplayingOriginalRequest(t *testing.T) {
	for _, route := range []string{"/collections/owned/points/query", "/collections/owned/points/scroll", "/collections/owned/points/count", "/collections/aliases"} {
		t.Run(route, func(t *testing.T) {
			entered, ack := make(chan struct{}), make(chan struct{})
			var calls atomic.Int32
			server := sessionServer(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				io.Copy(io.Discard, r.Body)
				close(entered)
				<-r.Context().Done()
				close(ack)
			})
			s := testSession(t, connections.QdrantConfig{Endpoint: server.URL}, "")
			l, _ := s.ReserveQuery(1)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			settled := make(chan contract.SQLResult, 1)
			go func() {
				result, err := l.Execute(ctx, "", "POST "+route+"\n{}")
				if !errors.Is(err, context.Canceled) || !errors.Is(err, contract.ErrSQLSessionLost) {
					t.Error("original cancellation lost", err)
				}
				settled <- result
			}()
			waitSessionSignal(t, entered)
			cancel()
			waitSessionSignal(t, ack)
			result := <-settled
			want := "interrupted"
			if route == "/collections/aliases" {
				want = "uncertain"
			}
			if result.Steps[0].State != want || calls.Load() != 1 {
				t.Fatal("read/mutation confused", result, calls.Load())
			}
			if _, err := l.Execute(context.Background(), "", "GET /"); !errors.Is(err, contract.ErrSQLSessionLost) || calls.Load() != 1 {
				t.Fatal("original request replayed", err)
			}
		})
	}
}
