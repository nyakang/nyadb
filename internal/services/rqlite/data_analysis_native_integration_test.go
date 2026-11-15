package rqlite

import (
	"context"
	"fmt"
	"math"
	"math/big"
	"slices"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func TestNativeRQLiteAnalysisPreservesExactMomentsDeclaredGroupsAndCompleteCounts(t *testing.T) {
	p := nativeFixture(t, "")
	s := openNative(t, p, "original-fixture-password")
	l := lease(t, s, 1)
	nativeExecute(t, l, `CREATE TABLE native_analysis(id INTEGER PRIMARY KEY,v,note TEXT COLLATE NOCASE,raw BLOB);
INSERT INTO native_analysis VALUES(1,9223372036854775805,'A',X'00FF'),(2,9223372036854775807,'a',X'00FF'),(3,NULL,NULL,NULL),(4,'3','other',zeroblob(8192));`)
	_, r := nativeDataDocument(t, s, "native_analysis")
	stats, err := r.Analyzer(context.Background(), p.ID, "main", "native_analysis", contract.DataOptions{}, []string{"v", "note", "raw"})
	if err != nil || stats.Rows != 4 || len(stats.Columns) != 3 {
		t.Fatal("native original analysis", err)
	}
	v := stats.Columns[0]
	if v.Numeric != 2 || v.Null != 1 || v.Distinct != 3 || v.Singletons != 3 || v.Min != "9223372036854775805" || v.Max != "9223372036854775807" || v.Mean != "9223372036854775806" || v.Median != "9223372036854775806" || v.StdDeviation != "1" {
		t.Fatal("native exact integer statistics changed", v)
	}
	note, raw := stats.Columns[1], stats.Columns[2]
	if note.Distinct != 2 || note.MaxFrequency != 2 || note.ModeCount != 1 || raw.MaxFrequency != 2 || raw.Distinct != 2 || len(raw.Distribution) != 2 || !raw.Distribution[1].Truncated {
		t.Fatal("original declared grouping or bounded BLOB distribution changed", note, raw)
	}
	filtered, err := r.Analyzer(context.Background(), p.ID, "main", "native_analysis", contract.DataOptions{Filters: []contract.DataFilterRule{{Column: "id", Mode: "less", Value: "3"}}}, []string{"v"})
	if err != nil || filtered.Rows != 2 || filtered.Columns[0].Mean != "9223372036854775806" {
		t.Fatal("analysis ignored original filters", err)
	}
	nativeExecute(t, l, "CREATE TABLE real_analysis(id INTEGER PRIMARY KEY,v);INSERT INTO real_analysis VALUES(1,CAST(0.1 AS REAL)),(2,CAST(1.5 AS REAL)),(3,-2),(4,CAST(-0.25 AS REAL)),(5,0),(6,NULL)")
	_, realRuntime := nativeDataDocument(t, s, "real_analysis")
	actual, err := realRuntime.Analyzer(context.Background(), p.ID, "main", "real_analysis", contract.DataOptions{}, []string{"v"})
	if err != nil {
		t.Fatal(err)
	}
	sum, squares := new(big.Rat), new(big.Rat)
	for _, v := range []float64{0.1, 1.5, -2, -0.25, 0} {
		n := new(big.Rat).SetFloat64(v)
		sum.Add(sum, n)
		squares.Add(squares, new(big.Rat).Mul(n, n))
	}
	mean := new(big.Rat).Quo(sum, big.NewRat(5, 1))
	variance := new(big.Rat).Sub(new(big.Rat).Quo(squares, big.NewRat(5, 1)), new(big.Rat).Mul(mean, mean))
	sd := new(big.Float).SetPrec(256).SetRat(variance)
	sd.Sqrt(sd)
	v = actual.Columns[0]
	if v.Numeric != 5 || v.Zero != 1 || v.Null != 1 || v.Mean != numericRatDisplay(mean) || v.StdDeviation != sd.Text('g', 32) || v.Median != "0" {
		t.Fatal("native REAL was reduced to its decimal display or floating SUM", v)
	}
	nativeExecute(t, l, "CREATE TABLE only_null(v);INSERT INTO only_null VALUES(NULL),(NULL)")
	_, nullRuntime := nativeDataDocument(t, s, "only_null")
	nulls, err := nullRuntime.Analyzer(context.Background(), p.ID, "main", "only_null", contract.DataOptions{}, []string{"v"})
	if err != nil || nulls.Columns[0].Null != 2 || nulls.Columns[0].Numeric != 0 || nulls.Columns[0].ModeCount != 1 || len(nulls.Columns[0].Modes) != 1 || !nulls.Columns[0].Modes[0].Null {
		t.Fatal("all-null original analysis", err)
	}
	if _, err := r.Analyzer(context.Background(), p.ID, "main", "native_analysis", contract.DataOptions{}, []string{"unknown"}); err == nil {
		t.Fatal("unknown analysis field accepted")
	}
	nativeExecute(t, l, "CREATE TABLE empty_analysis(v)")
	_, emptyRuntime := nativeDataDocument(t, s, "empty_analysis")
	empty, err := emptyRuntime.Analyzer(context.Background(), p.ID, "main", "empty_analysis", contract.DataOptions{}, []string{"v"})
	if err != nil || empty.Rows != 0 || empty.Columns[0].Numeric != 0 || empty.Columns[0].Mean != "" || empty.Columns[0].ModeCount != 0 {
		t.Fatal("empty native analysis invented values", err)
	}
	nativeExecute(t, l, "INSERT INTO real_analysis VALUES(7,1e999)")
	if _, err := realRuntime.Analyzer(context.Background(), p.ID, "main", "real_analysis", contract.DataOptions{}, []string{"v"}); err == nil || !strings.Contains(err.Error(), "finite") {
		t.Fatal("native non-finite analysis was accepted", err)
	}
}

func TestNativeRQLiteAnalysisExactInt64ExtremesSubnormalsFiniteMaximumAndZeros(t *testing.T) {
	p := nativeFixture(t, "")
	s := openNative(t, p, "original-fixture-password")
	l := lease(t, s, 1)
	integer := func(n int64) *big.Rat { return new(big.Rat).SetInt64(n) }
	real := func(n float64) *big.Rat { return new(big.Rat).SetFloat64(n) }
	for _, test := range []struct {
		name, input string
		values      []*big.Rat
		zeros       int64
	}{
		{"integer_extremes", "(-9223372036854775808),(9223372036854775807)", []*big.Rat{integer(math.MinInt64), integer(math.MaxInt64)}, 0},
		{"minimum_integer", "(-9223372036854775808)", []*big.Rat{integer(math.MinInt64)}, 0},
		{"finite_extremes", "(CAST(1.7976931348623157e308 AS REAL)),(CAST(-1.7976931348623157e308 AS REAL)),(CAST(5e-324 AS REAL)),(CAST(5e-324 AS REAL)),(0)", []*big.Rat{real(math.MaxFloat64), real(-math.MaxFloat64), real(math.SmallestNonzeroFloat64), real(math.SmallestNonzeroFloat64), integer(0)}, 1},
		{"recurring_subnormal_mean", "(CAST(5e-324 AS REAL)),(0),(0)", []*big.Rat{real(math.SmallestNonzeroFloat64), integer(0), integer(0)}, 2},
		{"numeric_zeros", "(0),(CAST(-0.0 AS REAL)),(CAST(0.0 AS REAL))", []*big.Rat{integer(0), real(math.Copysign(0, -1)), real(0)}, 3},
	} {
		t.Run(test.name, func(t *testing.T) {
			nativeExecute(t, l, "CREATE TABLE "+test.name+"(v);INSERT INTO "+test.name+" VALUES"+test.input)
			_, runtime := nativeDataDocument(t, s, test.name)
			analysis, err := runtime.Analyzer(context.Background(), p.ID, "main", test.name, contract.DataOptions{}, []string{"v"})
			if err != nil {
				t.Fatal("exact original native edge analysis", err)
			}
			sum, squares := new(big.Rat), new(big.Rat)
			for _, value := range test.values {
				sum.Add(sum, value)
				squares.Add(squares, new(big.Rat).Mul(value, value))
			}
			n := big.NewRat(int64(len(test.values)), 1)
			mean := new(big.Rat).Quo(sum, n)
			variance := new(big.Rat).Sub(new(big.Rat).Quo(squares, n), new(big.Rat).Mul(mean, mean))
			sd := new(big.Float).SetPrec(256).SetRat(variance)
			sd.Sqrt(sd)
			ordered := slices.Clone(test.values)
			slices.SortFunc(ordered, func(a, b *big.Rat) int { return a.Cmp(b) })
			median := new(big.Rat).Add(ordered[(len(ordered)-1)/2], ordered[len(ordered)/2])
			median.Quo(median, big.NewRat(2, 1))
			got := analysis.Columns[0]
			if got.Rows != int64(len(test.values)) || got.Numeric != got.Rows || got.Zero != test.zeros || got.Min != numericRatDisplay(ordered[0]) || got.Max != numericRatDisplay(ordered[len(ordered)-1]) || got.Mean != numericRatDisplay(mean) || got.Median != numericRatDisplay(median) || got.StdDeviation != sd.Text('g', 32) {
				t.Fatal("original exact moments changed", fmt.Sprintf("%+v", got))
			}
			if mean.Sign() != 0 && got.Mean == "0" {
				t.Fatal("nonzero exact mean was displayed as zero")
			}
		})
	}
}
