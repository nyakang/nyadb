package neo4j

import (
	"context"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	nd "github.com/neo4j/neo4j-go-driver/v6/neo4j"
	"github.com/nyakang/nyadb/internal/db/contract"
)

type fakeNativePlan struct {
	name     string
	args     map[string]any
	ids      []string
	children []nd.Plan
}

func (p *fakeNativePlan) Operator() string          { return p.name }
func (p *fakeNativePlan) Arguments() map[string]any { return p.args }
func (p *fakeNativePlan) Identifiers() []string     { return p.ids }
func (p *fakeNativePlan) Children() []nd.Plan       { return p.children }

type fakeNativeProfile struct {
	name     string
	args     map[string]any
	children []nd.QueryProfile
	metrics  contract.GraphPlanProfile
}

func (p *fakeNativeProfile) Operator() string            { return p.name }
func (p *fakeNativeProfile) Arguments() map[string]any   { return p.args }
func (*fakeNativeProfile) Identifiers() []string         { return []string{"n", "original"} }
func (p *fakeNativeProfile) Children() []nd.QueryProfile { return p.children }
func readFakeMetric(v *int64) (int64, bool) {
	if v == nil {
		return -789, false
	}
	return *v, true
}
func (p *fakeNativeProfile) Rows() (int64, bool)   { return readFakeMetric(p.metrics.Rows) }
func (p *fakeNativeProfile) DbHits() (int64, bool) { return readFakeMetric(p.metrics.DbHits) }
func (p *fakeNativeProfile) PageCacheHits() (int64, bool) {
	return readFakeMetric(p.metrics.PageCacheHits)
}
func (p *fakeNativeProfile) PageCacheMisses() (int64, bool) {
	return readFakeMetric(p.metrics.PageCacheMisses)
}
func (p *fakeNativeProfile) PageCacheHitRatio() (float64, bool) {
	if p.metrics.PageCacheHitRatio == nil {
		return math.NaN(), false
	}
	return *p.metrics.PageCacheHitRatio, true
}
func (p *fakeNativeProfile) Time() (time.Duration, bool) {
	if p.metrics.TimeNanoseconds == nil {
		return -time.Second, false
	}
	return time.Duration(*p.metrics.TimeNanoseconds), true
}

type nativePlanSummary struct {
	fakeSummary
	plan    nd.Plan
	profile nd.QueryProfile
}

func (s nativePlanSummary) Plan() nd.Plan                 { return s.plan }
func (s nativePlanSummary) QueryProfile() nd.QueryProfile { return s.profile }

func TestNativePlansCaptureSourceParametersOrderAndOwnedMetrics(t *testing.T) {
	zero, exact, ns := int64(0), int64(math.MaxInt64), int64(123456789)
	ratio := 0.0
	profile := &fakeNativeProfile{name: "ProduceResults", args: map[string]any{"EstimatedRows": float64(2), "Exact": exact, "Unknown": map[string]any{"nested": []any{int64(42), nil}}}, metrics: contract.GraphPlanProfile{Rows: &zero, DbHits: &exact, PageCacheHitRatio: &ratio, TimeNanoseconds: &ns}, children: []nd.QueryProfile{&fakeNativeProfile{name: "First"}, &fakeNativeProfile{name: "Second"}}}
	r := &fakeResult{keys: []string{"value"}, rows: []*nd.Record{{Values: []any{int64(42)}}}, summary: nativePlanSummary{fakeSummary: fakeSummary{agent: "Neo4j/5.26.21"}, profile: profile}}
	c, driver := fakeClient(t, r)
	source := "CYPHER runtime=slotted PROFILE RETURN $original"
	prepared := prepareTest(t, c, Request{Query: source, Parameters: map[string]any{"original": []byte{0, 255}}})
	out, err := c.Execute(context.Background(), prepared)
	if err != nil || !out.Completed || out.Uncertain || out.Summary.Plan == nil {
		t.Fatal(err, out.String())
	}
	p := out.Summary.Plan
	if p.Mode != "profile" || p.Query != source || p.Parameters[0].Value.Text != "AP8=" || p.Root.Children[0].Operator != "First" || p.Root.Children[1].Operator != "Second" || p.Root.Profile.Rows == nil || *p.Root.Profile.Rows != 0 || *p.Root.Profile.DbHits != exact || p.Root.Profile.PageCacheHits != nil || p.Root.Profile.PageCacheMisses != nil || *p.Root.Profile.TimeNanoseconds != ns {
		t.Fatal("native plan lost source, ordering, exactness or missing metrics")
	}
	before := out.Clone()
	profile.args["Exact"] = int64(1)
	profile.children = nil
	zero = 999
	exact = 1
	if !reflect.DeepEqual(out, before) {
		t.Fatal("plan followed driver buffers")
	}
	copy := c.LastResponse()
	*copy.Summary.Plan.Root.Profile.Rows = 888
	copy.Summary.Plan.Parameters[0].Value.Text = "later"
	if !reflect.DeepEqual(c.LastResponse(), before) {
		t.Fatal("native receipt shared plan ownership")
	}
	if _, err = c.Execute(context.Background(), prepared); !errors.Is(err, ErrUsed) || driver.session.runs.Load() != 1 {
		t.Fatal("plan was replayed", err)
	}
}

func TestNativePlanCaptureBoundsKeepNativeCompletionAndReceipt(t *testing.T) {
	root := &fakeNativePlan{name: "ProduceResults", args: map[string]any{"large": strings.Repeat("x", 2048)}}
	for _, kind := range []string{"bytes", "cycle", "nil_child", "metrics"} {
		t.Run(kind, func(t *testing.T) {
			summary := nativePlanSummary{fakeSummary: fakeSummary{agent: "Neo4j/5.26.21"}, plan: root}
			switch kind {
			case "cycle":
				p := &fakeNativePlan{name: "Cycle"}
				p.children = []nd.Plan{p}
				summary.plan = p
			case "nil_child":
				summary.plan = &fakeNativePlan{name: "Parent", children: []nd.Plan{nil}}
			case "metrics":
				negative := int64(-1)
				summary.plan = nil
				summary.profile = &fakeNativeProfile{name: "BadMetric", metrics: contract.GraphPlanProfile{Rows: &negative}}
			}
			r := &fakeResult{summary: summary}
			c, driver := fakeClient(t, r)
			out, err := c.Execute(context.Background(), prepareTest(t, c, Request{Query: "EXPLAIN RETURN 1", MaxBytes: 1024}))
			if err == nil || !r.consumed || !out.Attempted || !out.Completed || out.Uncertain || out.Summary.Plan != nil || out.Summary.PlanCaptureError == "" || out.Summary.Counters["nodes_created"] != 2 || len(out.Summary.Bookmarks) != 1 || driver.session.runs.Load() != 1 {
				t.Fatal("plan failure erased native completion or invented a partial plan", err, out.String())
			}
		})
	}
}

func TestNativeExplainPlansRemainIndependentOfTruncatedRows(t *testing.T) {
	root := &fakeNativePlan{name: "Root", args: map[string]any{"EstimatedRows": float64(0)}, children: []nd.Plan{&fakeNativePlan{name: "Left"}, &fakeNativePlan{name: "Right"}}}
	r := &fakeResult{keys: []string{"value"}, rows: []*nd.Record{{Values: []any{int64(1)}}, {Values: []any{int64(2)}}}, summary: nativePlanSummary{fakeSummary: fakeSummary{agent: "Neo4j/5.26.21"}, plan: root}}
	c, _ := fakeClient(t, r)
	out, err := c.Execute(context.Background(), prepareTest(t, c, Request{Query: "EXPLAIN RETURN 1", MaxRows: 1}))
	if err != nil || !out.Truncated || !out.Completed || len(out.Rows) != 1 || out.Summary.Plan.Mode != "explain" || out.Summary.Plan.Root.Profile != nil || out.Summary.PlanCaptureError != "" {
		t.Fatal("row truncation lost a complete plan", err)
	}
}
