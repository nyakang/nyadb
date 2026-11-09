package neo4j

import (
	"context"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"

	nd "github.com/neo4j/neo4j-go-driver/v6/neo4j"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/graphresult"
	"github.com/nyakang/nyadb/internal/db/queryplan"
)

// Observe the original native summary before conversion. The optimizer may
// reorder branches; our UI must retain the driver's order, not source order.
type nativePlanTapDriver struct {
	nd.Driver
	plan nd.Plan
}

func (d *nativePlanTapDriver) NewSession(ctx context.Context, cfg nd.SessionConfig) nd.Session {
	return nativePlanTapSession{Session: d.Driver.NewSession(ctx, cfg), owner: d}
}

type nativePlanTapSession struct {
	nd.Session
	owner *nativePlanTapDriver
}

func (s nativePlanTapSession) Run(ctx context.Context, q string, params map[string]any, config ...func(*nd.TransactionConfig)) (nd.Result, error) {
	r, e := s.Session.Run(ctx, q, params, config...)
	if e != nil {
		return r, e
	}
	return nativePlanTapResult{Result: r, owner: s.owner}, nil
}

type nativePlanTapResult struct {
	nd.Result
	owner *nativePlanTapDriver
}

func (r nativePlanTapResult) Consume(ctx context.Context) (nd.ResultSummary, error) {
	s, e := r.Result.Consume(ctx)
	if e == nil {
		r.owner.plan = s.Plan()
	}
	return s, e
}

func nativeOperatorsTest(p *contract.GraphExecutionPlan) []contract.GraphPlanOperator {
	var out []contract.GraphPlanOperator
	var visit func(contract.GraphPlanOperator)
	visit = func(n contract.GraphPlanOperator) {
		out = append(out, n)
		for _, child := range n.Children {
			visit(child)
		}
	}
	if p != nil {
		visit(p.Root)
	}
	return out
}

func TestNeo4jNativeStructuredPlansExplainProfileArgumentsAndOriginalReceipt(t *testing.T) {
	f := fixtureTest(t)
	c := nativeOpenTest(t, f)
	label, index := "NyaPlans_"+f.Nonce, "nya_plan_index_"+f.Nonce
	cleanup := func() { nativeRunTest(t, c, Request{Query: "MATCH (n:" + graphQuote(label) + ") DETACH DELETE n"}) }
	t.Cleanup(cleanup)
	source := "CYPHER runtime=slotted EXPLAIN CREATE (n:" + graphQuote(label) + " {exact:$exact, value:1}) RETURN n"
	parameters := map[string]any{"exact": int64(9223372036854775807)}
	planned := nativeRunTest(t, c, Request{Query: source, Parameters: parameters})
	if planned.Summary.Plan == nil || planned.Summary.Plan.Mode != "explain" || planned.Summary.Plan.Query != source || planned.Summary.Plan.Parameters[0].Value.Text != "9223372036854775807" || planned.Summary.Counters["nodes_created"] != 0 || len(planned.Rows) != 0 {
		t.Fatal("EXPLAIN lost exact original request or executed source")
	}
	if nativeRunTest(t, c, Request{Query: "MATCH (n:" + graphQuote(label) + ") RETURN count(n)"}).Rows[0][0].Text != "0" {
		t.Fatal("EXPLAIN created data")
	}
	projected, err := queryplan.Parse("neo4j", graphresult.Project(source, planned))
	if err != nil || len(projected.Nodes) != 1 {
		t.Fatal("native plan did not project", err)
	}
	profileSource := strings.Replace(source, " EXPLAIN ", " PROFILE ", 1)
	profiled := nativeRunTest(t, c, Request{Query: profileSource, Parameters: parameters})
	if profiled.Summary.Plan == nil || profiled.Summary.Plan.Mode != "profile" || profiled.Summary.Counters["nodes_created"] != 1 || len(profiled.Rows) != 1 || profiled.Rows[0][0].Node.Properties[0].Value.Text != "9223372036854775807" || len(profiled.Summary.Bookmarks) == 0 {
		t.Fatal("PROFILE lost actual query, types or write completion")
	}
	if nativeRunTest(t, c, Request{Query: "MATCH (n:" + graphQuote(label) + ") RETURN count(n)"}).Rows[0][0].Text != "1" {
		t.Fatal("PROFILE did not execute exactly once")
	}
	profile := profiled.Summary.Plan
	if profile.Root.Profile == nil || profile.Root.Profile.Rows == nil || *profile.Root.Profile.Rows != 1 {
		t.Fatal("native profile metrics missing")
	}
	frozen := profile.Clone()
	copy := c.LastResponse()
	if copy.Summary.Plan != nil {
		copy.Summary.Plan.Root.Operator = "later"
	}
	if !reflect.DeepEqual(profile, frozen) {
		t.Fatal("native plans share later receipt buffers")
	}
	nativeRunTest(t, c, Request{Query: "CREATE INDEX " + graphQuote(index) + " FOR (n:" + graphQuote(label) + ") ON (n.value)"})
	t.Cleanup(func() { nativeRunTest(t, c, Request{Query: "DROP INDEX " + graphQuote(index)}) })
	nativeRunTest(t, c, Request{Query: "CALL db.awaitIndexes(30)"})
	seek := nativeRunTest(t, c, Request{Query: "EXPLAIN MATCH (n:" + graphQuote(label) + ") USING INDEX n:" + graphQuote(label) + "(value) WHERE n.value=$value RETURN n", Parameters: map[string]any{"value": int64(1)}})
	found := false
	for _, n := range nativeOperatorsTest(seek.Summary.Plan) {
		if strings.Contains(n.Operator, "IndexSeek") {
			found = true
		}
	}
	if !found {
		t.Fatal("original native index seek not retained")
	}
	tap := &nativePlanTapDriver{Driver: c.driver}
	c.driver = tap
	union := nativeRunTest(t, c, Request{Query: "EXPLAIN RETURN 1 AS x UNION ALL RETURN 2 AS x"})
	var nativeUnion nd.Plan
	var findUnion func(nd.Plan)
	findUnion = func(p nd.Plan) {
		if p == nil {
			return
		}
		if strings.Contains(p.Operator(), "Union") {
			nativeUnion = p
		}
		for _, child := range p.Children() {
			findUnion(child)
		}
	}
	findUnion(tap.plan)
	found = false
	for _, n := range nativeOperatorsTest(union.Summary.Plan) {
		if strings.Contains(n.Operator, "Union") && len(n.Children) == 2 {
			if nativeUnion == nil || len(nativeUnion.Children()) != 2 {
				t.Fatal("missing original native union branches")
			}
			var compare func(contract.GraphPlanOperator, nd.Plan)
			compare = func(retained contract.GraphPlanOperator, original nd.Plan) {
				if retained.Operator != original.Operator() || !reflect.DeepEqual(retained.Identifiers, original.Identifiers()) || len(retained.Children) != len(original.Children()) {
					t.Fatal("native branch shape or order changed")
				}
				for _, arg := range retained.Arguments {
					switch value := original.Arguments()[arg.Name].(type) {
					case string:
						if arg.Value.Text != value {
							t.Fatal("native branch details changed")
						}
					case int64:
						if arg.Value.Text != strconv.FormatInt(value, 10) {
							t.Fatal("native branch identity or order changed")
						}
					}
				}
				for i, child := range retained.Children {
					compare(child, original.Children()[i])
				}
			}
			for i, child := range n.Children {
				compare(child, nativeUnion.Children()[i])
			}
			found = true
		}
	}
	if !found {
		t.Fatal("native ordered union tree missing")
	}
	for _, query := range []string{"EXPLAIN MATCH (n:" + graphQuote(label) + ") RETURN count(n)", "EXPLAIN CALL { RETURN 1 AS x } RETURN x", "EXPLAIN MATCH (a:" + graphQuote(label) + "),(b:" + graphQuote(label) + ") WHERE a.value=b.value RETURN a,b"} {
		r := nativeRunTest(t, c, Request{Query: query})
		if p, e := queryplan.Parse("neo4j", graphresult.Project(query, r)); e != nil || len(p.Nodes) == 0 {
			t.Fatal("native aggregation/subquery/join plan missing", e)
		}
	}
	zero := nativeRunTest(t, c, Request{Query: "PROFILE MATCH (n:" + graphQuote(label) + ") WHERE n.value=-1 RETURN n"})
	if zero.Summary.Plan.Root.Profile.Rows == nil || *zero.Summary.Plan.Root.Profile.Rows != 0 {
		t.Fatal("native recorded zero became unknown")
	}
	truncated := nativeRunTest(t, c, Request{Query: "PROFILE UNWIND range(1,3) AS x RETURN x", MaxRows: 1})
	if !truncated.Truncated || len(truncated.Rows) != 1 || truncated.Summary.Plan == nil || truncated.Summary.Plan.Root.Profile.Rows == nil || *truncated.Summary.Plan.Root.Profile.Rows != 3 {
		t.Fatal("row limit changed complete native profile")
	}
	if _, e := queryplan.Parse("neo4j", graphresult.Project(truncated.Summary.Plan.Query, truncated)); e != nil {
		t.Fatal("complete truncated-row native plan rejected", e)
	}
	bad, e := c.Execute(context.Background(), prepareTest(t, c, Request{Query: "EXPLAIN MATCH ( RETURN n"}))
	if e == nil || bad.Completed || bad.Summary.Plan != nil || bad.FailureCode == "" {
		t.Fatal("syntax error fabricated native plan")
	}
}

func TestNeo4jNativeStructuredPlansBoundsAndCompletionOwnership(t *testing.T) {
	f := fixtureTest(t)
	c := nativeOpenTest(t, f)
	source := "EXPLAIN UNWIND range(1,100) AS x RETURN x ORDER BY x"
	prepared := prepareTest(t, c, Request{Query: source, MaxBytes: 128})
	r, e := c.Execute(context.Background(), prepared)
	if !errors.Is(e, ErrBounds) || !r.Attempted || !r.Completed || r.Uncertain || r.Summary.Plan != nil || r.Summary.PlanCaptureError == "" || r.Summary.Database != "neo4j" {
		t.Fatal("bounded plan failure erased real completion", e, r.String())
	}
	if _, e = c.Execute(context.Background(), prepared); !errors.Is(e, ErrUsed) {
		t.Fatal("failed plan capture replayed", e)
	}
	marker := "nya_plan_bounds_" + f.Nonce
	defer cleanupMarkerTest(t, c, marker)
	write := prepareTest(t, c, Request{Query: "PROFILE CREATE (n:NyaFoundation {marker:$marker}) RETURN n", Parameters: map[string]any{"marker": marker}, MaxBytes: 128})
	receipt, e := c.Execute(context.Background(), write)
	if !errors.Is(e, ErrBounds) || !receipt.Completed || receipt.Uncertain || receipt.Summary.Counters["nodes_created"] != 1 || receipt.Summary.PlanCaptureError == "" || receipt.Summary.Plan != nil || len(receipt.Summary.Bookmarks) == 0 {
		t.Fatal("plan bounds erased actual confirmed PROFILE write", e)
	}
	if _, e = c.Execute(context.Background(), write); !errors.Is(e, ErrUsed) {
		t.Fatal("bounded PROFILE replayed", e)
	}
	count := nativeRunTest(t, c, Request{Query: "MATCH (n:NyaFoundation {marker:$marker}) RETURN count(n)", Parameters: map[string]any{"marker": marker}})
	if count.Rows[0][0].Text != "1" {
		t.Fatal("confirmed bounded PROFILE write count changed")
	}
	after := nativeRunTest(t, c, Request{Query: "RETURN 42"})
	if after.Summary.Plan != nil || after.Summary.PlanCaptureError != "" {
		t.Fatal("ordinary query inherited old plan")
	}
}
