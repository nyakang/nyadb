package rqlite

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"math/big"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

// At most eight octet products share an exponent. This bound keeps native
// integer products and SUM coefficients exact, never silently promoted to REAL.
const exactNumericRowsLimit = int64(math.MaxInt64 / (65025 * 8))

func numericPredicate(value string) string { return "typeof(" + value + ") IN ('integer','real')" }

// Normalize finite REAL by exact powers of two, extract its integer significand
// and exponent, then return only aggregated signed octet coefficients. Original
// INTEGER values retain all 64 bits. No raw numeric rows or floating SUM/AVG
// leave the server; Go combines bounded exact dyadic bins into rational moments.
func numericBinsSQL(value, from string) string {
	// SQLite abs(MinInt64) raises integer overflow. CASE also prevents the
	// planner from evaluating that expression for INTEGER storage classes.
	finite := "CASE WHEN typeof(" + value + ")='real' THEN abs(" + value + ")<=1.7976931348623157e308 ELSE 1 END"
	return "WITH RECURSIVE _nyadb_numeric AS MATERIALIZED (SELECT row_number() OVER () AS id," + value + " AS v,count(*) AS n,typeof(" + value + ") AS typ" + from + " AND " + numericPredicate(value) + " AND " + finite + " GROUP BY typeof(" + value + ")," + dataBytes(value) + ")," +
		"_nyadb_scale(id,v,n,typ,m,e) AS (SELECT id,v,n,typ,CASE WHEN typ='integer' THEN v ELSE abs(v) END,0 FROM _nyadb_numeric UNION ALL SELECT id,v,n,typ,CASE WHEN m<1 THEN m*2 ELSE m/2 END,CASE WHEN m<1 THEN e-1 ELSE e+1 END FROM _nyadb_scale WHERE typ='real' AND m!=0 AND (m<1 OR m>=2) AND e BETWEEN -1074 AND 1024)," +
		"_nyadb_significands AS MATERIALIZED (SELECT id,n,CASE WHEN typ='integer' THEN m ELSE CAST(m*4503599627370496 AS INTEGER)*CASE WHEN v<0 THEN -1 ELSE 1 END END AS m,CASE WHEN typ='integer' THEN 0 ELSE e-52 END AS e FROM _nyadb_scale WHERE typ='integer' OR m=0 OR (m>=1 AND m<2))," +
		"_nyadb_octets(i) AS (VALUES(0),(1),(2),(3),(4),(5),(6),(7))," +
		fmt.Sprintf("_nyadb_parts AS MATERIALIZED (SELECT id,e,n,i,CASE WHEN i=7 THEN m>>56 ELSE (m>>(i*8))&255 END AS d FROM _nyadb_significands CROSS JOIN _nyadb_octets WHERE m!=0 AND (SELECT coalesce(sum(n),0) FROM _nyadb_numeric)<=%d) ", exactNumericRowsLimit) +
		"SELECT 0,e+i*8 AS exponent,sum(d*n) FROM _nyadb_parts GROUP BY exponent UNION ALL SELECT 1,a.e+b.e+(a.i+b.i)*8 AS exponent,sum(a.d*b.d*a.n) FROM _nyadb_parts a JOIN _nyadb_parts b ON a.id=b.id GROUP BY exponent"
}

func numericRatCell(c contract.Cell) (*big.Rat, error) {
	switch c.Kind {
	case contract.Integer:
		n, err := strconv.ParseInt(c.Text, 10, 64)
		if err != nil {
			return nil, errIncomplete
		}
		return new(big.Rat).SetInt64(n), nil
	case contract.Floating:
		n, err := strconv.ParseFloat(c.Text, 64)
		if err != nil || math.IsNaN(n) || math.IsInf(n, 0) {
			return nil, contract.ErrUnsupported
		}
		return new(big.Rat).SetFloat64(n), nil
	default:
		return nil, errIncomplete
	}
}

func numericRatDisplay(v *big.Rat) string {
	if v.IsInt() {
		return v.Num().String()
	}
	denominator := new(big.Int).Set(v.Denom())
	powers := [2]int{}
	for i, factor := range []*big.Int{big.NewInt(2), big.NewInt(5)} {
		for {
			quotient, remainder := new(big.Int), new(big.Int)
			quotient.QuoRem(denominator, factor, remainder)
			if remainder.Sign() != 0 {
				break
			}
			denominator = quotient
			powers[i]++
			if powers[i] > 4096 {
				break
			}
		}
	}
	if denominator.Cmp(big.NewInt(1)) == 0 && max(powers[0], powers[1]) <= 4096 {
		return strings.TrimRight(strings.TrimRight(v.FloatString(max(powers[0], powers[1])), "0"), ".")
	}
	// A recurring mean needs significant digits: fixed fractional precision
	// would silently display a nonzero subnormal mean as zero.
	return new(big.Float).SetPrec(256).SetRat(v).Text('g', 32)
}

func decodeNumericBins(r receipt) ([2]*big.Rat, error) {
	out := [2]*big.Rat{new(big.Rat), new(big.Rat)}
	rows, err := decodedRows(r, 0, 3)
	if err != nil || len(rows) > 9000 {
		return out, errIncomplete
	}
	seen := map[[2]int64]bool{}
	for _, row := range rows {
		which, err := cellInt(row[0])
		e, e2 := cellInt(row[1])
		n, e3 := cellInt(row[2])
		if err != nil || e2 != nil || e3 != nil || which < 0 || which > 1 || e < -2400 || e > 2200 || seen[[2]int64{which, e}] {
			return out, errIncomplete
		}
		seen[[2]int64{which, e}] = true
		coefficient := big.NewInt(n)
		value := new(big.Rat)
		if e >= 0 {
			value.SetInt(coefficient.Lsh(coefficient, uint(e)))
		} else {
			value.SetFrac(coefficient, new(big.Int).Lsh(big.NewInt(1), uint(-e)))
		}
		out[which].Add(out[which], value)
	}
	return out, nil
}

func analysisStatements(value, from string) []any {
	predicate := numericPredicate(value)
	numericFrom := from + " AND " + predicate
	groups := "SELECT " + value + " AS v,count(*) AS n" + from + " AND " + value + " IS NOT NULL GROUP BY " + value
	queryValue := func(kind int, source string) string {
		return fmt.Sprintf("SELECT %d,typeof(v),hex(%s) FROM (%s)", kind, dataBytes("v"), source)
	}
	central := queryValue(0, "SELECT "+value+" AS v"+numericFrom+" ORDER BY "+value+" ASC LIMIT 1") + " UNION ALL " + queryValue(1, "SELECT "+value+" AS v"+numericFrom+" ORDER BY "+value+" DESC LIMIT 1") + " UNION ALL " + queryValue(2, "SELECT "+value+" AS v"+numericFrom+" ORDER BY "+value+" LIMIT (2-(SELECT count(*)"+numericFrom+")%2) OFFSET ((SELECT count(*)"+numericFrom+")-1)/2")
	return []any{
		"SELECT count(*),count(*)-count(" + value + "),count(CASE WHEN " + predicate + " THEN 1 END),count(CASE WHEN " + predicate + " AND " + value + "=0 THEN 1 END),count(CASE WHEN typeof(" + value + ")='real' THEN CASE WHEN abs(" + value + ")>1.7976931348623157e308 THEN 1 END END)" + from,
		"SELECT count(*),count(CASE WHEN n=1 THEN 1 END),coalesce(max(mx),0),count(CASE WHEN n=mx THEN 1 END) FROM (SELECT n,max(n) OVER () AS mx FROM (" + groups + "))",
		central,
		numericBinsSQL(value, from),
		"SELECT typeof(v),hex(substr(" + dataBytes("v") + ",1,128)),length(" + dataBytes("v") + "),n FROM (" + groups + ") ORDER BY n DESC,+v ASC LIMIT 10",
	}
}

func analysisSlice(r receipt, index int) receipt { r.Results = r.Results[index : index+1]; return r }

func decodeColumnAnalysis(r receipt, c contract.StructureColumn) (contract.DataColumnAnalysis, error) {
	stats := contract.DataColumnAnalysis{Name: c.Name, Kind: c.Type, Collation: "SQLite declared grouping; numeric storage classes only"}
	if len(r.Results) != 5 {
		return stats, errIncomplete
	}
	counts, err := decodedRows(r, 0, 5)
	if err != nil || len(counts) != 1 {
		return stats, errIncomplete
	}
	values := make([]int64, 5)
	for i := range values {
		values[i], err = cellInt(counts[0][i])
		if err != nil || values[i] < 0 {
			return stats, errIncomplete
		}
	}
	stats.Rows, stats.Null, stats.Numeric, stats.Zero = values[0], values[1], values[2], values[3]
	if stats.Null > stats.Rows || stats.Numeric > stats.Rows-stats.Null || stats.Zero > stats.Numeric || values[4] > stats.Numeric {
		return stats, errIncomplete
	}
	if values[4] > 0 || stats.Numeric > exactNumericRowsLimit {
		return stats, errors.New("RQLite numeric statistics exceed finite exact aggregation bounds")
	}
	grouped, err := decodedRows(analysisSlice(r, 1), 0, 4)
	if err != nil || len(grouped) != 1 {
		return stats, errIncomplete
	}
	groupCounts := make([]int64, 4)
	for i := range groupCounts {
		groupCounts[i], err = cellInt(grouped[0][i])
		if err != nil || groupCounts[i] < 0 {
			return stats, errIncomplete
		}
	}
	stats.Distinct, stats.Singletons, stats.MaxFrequency, stats.ModeCount = groupCounts[0], groupCounts[1], groupCounts[2], groupCounts[3]
	if stats.Singletons > stats.Distinct || stats.Distinct > stats.Rows-stats.Null || stats.MaxFrequency > stats.Rows-stats.Null || stats.ModeCount > stats.Distinct {
		return stats, errIncomplete
	}
	nonNull := stats.Rows - stats.Null
	if nonNull == 0 && (stats.Distinct != 0 || stats.MaxFrequency != 0 || stats.ModeCount != 0) || nonNull > 0 && (stats.Distinct == 0 || stats.MaxFrequency == 0 || stats.ModeCount == 0) {
		return stats, errIncomplete
	}
	if stats.MaxFrequency > 0 && stats.ModeCount > nonNull/stats.MaxFrequency || stats.MaxFrequency == 1 && stats.Singletons != stats.Distinct {
		return stats, errIncomplete
	}
	if stats.Null > stats.MaxFrequency {
		stats.MaxFrequency, stats.ModeCount = stats.Null, 0
	}
	if stats.Numeric > 0 {
		central, err := decodedRows(analysisSlice(r, 2), 0, 3)
		if err != nil || len(central) != int(4-stats.Numeric%2) {
			return stats, errIncomplete
		}
		var minimum, maximum *big.Rat
		median := new(big.Rat)
		medianRows := int64(0)
		for _, row := range central {
			kind, err := cellInt(row[0])
			typ, e := cellText(row[1])
			raw, e2 := cellText(row[2])
			if err != nil || e != nil || e2 != nil || len(raw) > 1024 {
				return stats, errIncomplete
			}
			b, err := hex.DecodeString(raw)
			if err != nil {
				return stats, errIncomplete
			}
			v, err := storageDataCell(typ, b, false)
			if err != nil {
				return stats, err
			}
			n, err := numericRatCell(v)
			if err != nil {
				return stats, err
			}
			switch kind {
			case 0:
				if minimum != nil {
					return stats, errIncomplete
				}
				minimum = n
			case 1:
				if maximum != nil {
					return stats, errIncomplete
				}
				maximum = n
			case 2:
				median.Add(median, n)
				medianRows++
			default:
				return stats, errIncomplete
			}
		}
		if minimum == nil || maximum == nil || minimum.Cmp(maximum) > 0 || medianRows != 2-stats.Numeric%2 {
			return stats, errIncomplete
		}
		moments, err := decodeNumericBins(analysisSlice(r, 3))
		if err != nil {
			return stats, err
		}
		n := new(big.Rat).SetInt64(stats.Numeric)
		mean := new(big.Rat).Quo(moments[0], n)
		variance := new(big.Rat).Sub(new(big.Rat).Quo(moments[1], n), new(big.Rat).Mul(mean, mean))
		median.Quo(median, new(big.Rat).SetInt64(medianRows))
		if variance.Sign() < 0 || mean.Cmp(minimum) < 0 || mean.Cmp(maximum) > 0 || median.Cmp(minimum) < 0 || median.Cmp(maximum) > 0 {
			return stats, errIncomplete
		}
		if moments[1].Sign() < 0 || stats.Zero < stats.Numeric && moments[1].Sign() == 0 || minimum.Cmp(maximum) == 0 && variance.Sign() != 0 {
			return stats, errIncomplete
		}
		if stats.Zero == stats.Numeric && (moments[0].Sign() != 0 || moments[1].Sign() != 0 || minimum.Sign() != 0 || maximum.Sign() != 0) {
			return stats, errIncomplete
		}
		deviation := new(big.Float).SetPrec(256).SetRat(variance)
		deviation.Sqrt(deviation)
		stats.Min, stats.Max, stats.Mean, stats.Median, stats.StdDeviation = numericRatDisplay(minimum), numericRatDisplay(maximum), numericRatDisplay(mean), numericRatDisplay(median), deviation.Text('g', 32)
	} else {
		central, err := decodedRows(analysisSlice(r, 2), 0, 3)
		bins, e := decodedRows(analysisSlice(r, 3), 0, 3)
		if err != nil || e != nil || len(central) != 0 || len(bins) != 0 {
			return stats, errIncomplete
		}
	}
	distribution, err := decodedRows(analysisSlice(r, 4), 0, 4)
	if err != nil || int64(len(distribution)) != min(stats.Distinct, 10) {
		return stats, errIncomplete
	}
	distributed, previousCount := int64(0), nonNull
	for _, row := range distribution {
		typ, err := cellText(row[0])
		raw, e := cellText(row[1])
		length, e2 := cellInt(row[2])
		count, e3 := cellInt(row[3])
		if err != nil || e != nil || e2 != nil || e3 != nil || length < 0 || count < 1 || count > previousCount || count > nonNull-distributed || int64(len(raw)) != 2*min(length, 128) {
			return stats, errIncomplete
		}
		distributed, previousCount = distributed+count, count
		b, err := hex.DecodeString(raw)
		if err != nil {
			return stats, errIncomplete
		}
		v, err := storageDataCell(typ, b, length > 128)
		if err != nil {
			return stats, err
		}
		frequency := contract.DataAnalysisFrequency{Value: v.Display(), Count: count, Truncated: v.Truncated}
		stats.Distribution = append(stats.Distribution, frequency)
		if count == stats.MaxFrequency {
			stats.Modes = append(stats.Modes, frequency)
		}
	}
	if stats.Distinct <= 10 && distributed != nonNull {
		return stats, errIncomplete
	}
	if stats.MaxFrequency > 0 && stats.Null == stats.MaxFrequency {
		stats.ModeCount++
		stats.Modes = append(stats.Modes, contract.DataAnalysisFrequency{Null: true, Count: stats.Null})
	}
	return stats, nil
}

func (s *Session) analyzeData(ctx context.Context, id int64, db, table string, o contract.DataOptions, fields []string) (*contract.DataAnalysis, error) {
	if o.Offset != 0 || o.Limit != 0 || o.After != nil || o.UseKeyset || len(fields) == 0 || len(fields) > maxDataColumns {
		return nil, connections.ErrInvalid
	}
	started := time.Now()
	t, err := s.dataTable(ctx, id, db, table, o.Object)
	if err != nil {
		return nil, err
	}
	if _, err := dataOrder(t, o); err != nil {
		return nil, err
	}
	where, err := dataWhere(t, o, false)
	if err != nil {
		return nil, err
	}
	if where == "" {
		where = "1"
	}
	from := " FROM main." + quote(table) + " WHERE " + where
	statements := []any{"SELECT " + schemaGuard(t)}
	seen := map[string]bool{}
	for _, field := range fields {
		if seen[field] || !slices.Contains(columnNames(t), field) {
			return nil, connections.ErrInvalid
		}
		seen[field] = true
		statements = append(statements, analysisStatements("+"+quote(field), from)...)
	}
	r, err := s.request(ctx, "/db/query", statements, true, false)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	guard, err := decodedRows(r, 0, 1)
	if err != nil || len(guard) != 1 || guard[0][0].Text != "1" {
		return nil, contract.ErrDataConflict
	}
	out := &contract.DataAnalysis{}
	for i, field := range fields {
		part := r
		part.Results = r.Results[1+i*5 : 1+(i+1)*5]
		stats, err := decodeColumnAnalysis(part, t.structure.Columns[slices.Index(columnNames(t), field)])
		if err != nil {
			return nil, err
		}
		if i > 0 && stats.Rows != out.Rows {
			return nil, errIncomplete
		}
		out.Rows = stats.Rows
		out.Columns = append(out.Columns, stats)
	}
	out.Duration, out.CompletedAt = time.Since(started), time.Now()
	return out, nil
}
