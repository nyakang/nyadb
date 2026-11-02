package engines

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math/big"
	"strings"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

// Aggregation, grouping and sorting run in the engine, in one read snapshot.
// Only statistics, two median values and ten bounded frequencies are returned.
func (s *Session) analyzeData(ctx context.Context, id int64, schema, name string, o contract.DataOptions, fields []string) (*contract.DataAnalysis, error) {
	if o.Offset != 0 || o.Limit != 0 || o.After != nil || o.UseKeyset || len(fields) == 0 || len(fields) > 128 {
		return nil, connections.ErrInvalid
	}
	r, err := s.pageRequest(id, schema, name, o)
	if err != nil {
		return nil, err
	}
	var out *contract.DataAnalysis
	started := time.Now()
	err = s.dataQuery(ctx, r.Object, nil, func(ctx context.Context, q queryer) error {
		t, err := s.structure(ctx, q, r.Object)
		if err != nil {
			return err
		}
		cols := validColumns(t)
		seen := map[string]bool{}
		for _, f := range fields {
			if _, ok := cols[f]; !ok || seen[f] {
				return connections.ErrInvalid
			}
			seen[f] = true
		}
		for _, sort := range r.Sorts {
			if _, ok := cols[sort.Column]; !ok {
				return connections.ErrInvalid
			}
		}
		where, args, err := s.dataWhere(t, r)
		if err != nil {
			return err
		}
		from := " FROM " + qualified(t.Object) + " WHERE " + where
		out = &contract.DataAnalysis{}
		for _, field := range fields {
			if err = ctx.Err(); err != nil {
				return err
			}
			stats, err := s.analyzeColumn(ctx, q, cols[field], from, args)
			if err != nil {
				return err
			}
			out.Rows = stats.Rows
			out.Columns = append(out.Columns, stats)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	out.CompletedAt = time.Now()
	out.Duration = time.Since(started)
	return out, nil
}
func (s *Session) analyzeColumn(ctx context.Context, q queryer, f contract.Field, from string, args []any) (contract.DataColumnAnalysis, error) {
	c := quote(f.Name)
	group := c
	stats := contract.DataColumnAnalysis{Name: f.Name, Kind: f.DatabaseType}
	if s.kind == connections.PostgreSQL {
		if !pgDataScalar(f.DatabaseType) && !pgDataNumeric(f.DatabaseType) && canonicalDataType(f.DatabaseType) != "bytea" {
			group = c + `::text COLLATE "C"`
			stats.Collation = "C (text representation)"
		} else if canonicalDataType(f.DatabaseType) == "bytea" {
			stats.Collation = "binary"
		}
	} else {
		stats.Collation = "SQLite declared grouping; numeric storage classes only"
	}
	grouped := "SELECT " + group + " AS v, COUNT(*) AS n" + from + " AND " + c + " IS NOT NULL GROUP BY " + group
	aggregate := "SELECT COUNT(*),COUNT(*)-COUNT(" + c + ")" + from
	if err := queryOne(ctx, q, aggregate, args, &stats.Rows, &stats.Null); err != nil {
		return stats, err
	}
	if err := queryOne(ctx, q, "SELECT COUNT(*),COUNT(CASE WHEN n=1 THEN 1 END) FROM ("+grouped+") AS nyadb_groups", args, &stats.Distinct, &stats.Singletons); err != nil {
		return stats, err
	}
	if s.kind == connections.SQLite {
		var raw string
		if err := queryOne(ctx, q, "SELECT "+sqliteNumericFunction+"("+c+")"+from, args, &raw); err != nil {
			return stats, err
		}
		var numeric numericSummary
		if err := json.Unmarshal([]byte(raw), &numeric); err != nil {
			return stats, err
		}
		stats.Numeric, stats.Zero, stats.Min, stats.Max, stats.Mean, stats.StdDeviation = numeric.Count, numeric.Zero, numeric.Min, numeric.Max, numeric.Mean, numeric.Deviation
	} else if pgDataNumeric(f.DatabaseType) {
		v := c + "::numeric"
		bounded := func(expr string) string { return "substring((" + expr + ")::text from 1 for 4097)" }
		text := "SELECT COUNT(" + c + "),COUNT(CASE WHEN " + c + "=0 THEN 1 END)," + bounded("MIN("+v+")") + "," + bounded("MAX("+v+")") + "," + bounded("AVG("+v+")") + "," + bounded("STDDEV_POP("+v+")") + from
		var min, max, mean, sd sql.NullString
		if err := queryOne(ctx, q, text, args, &stats.Numeric, &stats.Zero, &min, &max, &mean, &sd); err != nil {
			return stats, err
		}
		stats.Min, stats.Max, stats.Mean, stats.StdDeviation = min.String, max.String, mean.String, sd.String
		for _, n := range []string{stats.Min, stats.Max, stats.Mean, stats.StdDeviation} {
			if len(n) > 4096 || n == "NaN" || strings.Contains(n, "Infinity") {
				return stats, errors.New("numeric statistics exceed supported finite precision")
			}
		}
	}
	if stats.Numeric > 0 {
		predicate := c + " IS NOT NULL"
		value := c
		if s.kind == connections.SQLite {
			predicate = "typeof(" + c + ") IN ('integer','real')"
		} else {
			value = "substring(" + c + "::text from 1 for 4097)"
		}
		// Numeric order stays numeric; cast only the two returned central values.
		text := "SELECT " + value + from + " AND " + predicate + " ORDER BY " + c + " LIMIT " + s.arg(len(args)+1) + " OFFSET " + s.arg(len(args)+2)
		medianArgs := append(append([]any(nil), args...), int64(2-stats.Numeric%2), (stats.Numeric-1)/2)
		rows, err := q.QueryContext(ctx, text, medianArgs...)
		if err != nil {
			return stats, err
		}
		sum := new(big.Rat)
		count := int64(0)
		for rows.Next() {
			var raw any
			if err = rows.Scan(&raw); err != nil {
				break
			}
			var v *big.Rat
			if s.kind == connections.SQLite {
				switch n := raw.(type) {
				case int64:
					v, err = numericRat(n)
				case float64:
					v, err = numericRat(n)
				}
			} else {
				n := cell(raw, "numeric", s.kind).Text
				if len(n) > 4096 {
					err = errors.New("median exceeds supported precision")
				} else {
					var ok bool
					v, ok = new(big.Rat).SetString(n)
					if !ok {
						err = errors.New("non-finite median is unsupported")
					}
				}
			}
			if err != nil {
				break
			}
			if v == nil {
				err = errors.New("invalid numeric median")
				break
			}
			sum.Add(sum, v)
			count++
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()
		if err != nil {
			return stats, err
		}
		if count != 2-stats.Numeric%2 {
			return stats, errors.New("inconsistent numeric snapshot")
		}
		stats.Median = ratDisplay(sum.Quo(sum, new(big.Rat).SetInt64(count)))
	}
	var preview, storage, truncated string
	if s.kind == connections.SQLite {
		preview = "CASE WHEN typeof(v)='text' AND instr(v,char(0))>0 THEN _nyadb_text_slice_v1(v,1,128) WHEN typeof(v) IN ('text','blob') AND length(v)>0 THEN substr(v,1,128) ELSE v END"
		storage = "typeof(v)"
		truncated = "CASE WHEN typeof(v)='text' AND instr(v,char(0))>0 THEN _nyadb_text_units_v1(v)>128 WHEN typeof(v) IN ('text','blob') THEN length(v)>128 ELSE 0 END"
	} else {
		preview = "substring(v::text from 1 for 128)"
		storage = "'text'"
		truncated = "char_length(v::text)>128"
		if canonicalDataType(f.DatabaseType) == "bytea" {
			preview = "substring(v from 1 for 128)"
			storage = "'blob'"
			truncated = "octet_length(v)>128"
		}
	}
	rows, err := q.QueryContext(ctx, "SELECT "+preview+",n,"+storage+","+truncated+" FROM ("+grouped+") AS nyadb_groups ORDER BY n DESC,v ASC LIMIT 10", args...)
	if err != nil {
		return stats, err
	}
	for rows.Next() {
		var raw any
		var kind string
		var frequency contract.DataAnalysisFrequency
		if err = rows.Scan(&raw, &frequency.Count, &kind, &frequency.Truncated); err != nil {
			break
		}
		if kind == "blob" {
			v, ok := raw.([]byte)
			if !ok {
				err = errors.New("invalid binary frequency")
				break
			}
			frequency.Value = "0x" + hex.EncodeToString(v)
		} else {
			frequency.Value = cell(raw, f.DatabaseType, s.kind).Display()
		}
		stats.Distribution = append(stats.Distribution, frequency)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return stats, err
	}
	if len(stats.Distribution) > 0 {
		stats.MaxFrequency = stats.Distribution[0].Count
	}
	stats.MaxFrequency = max(stats.MaxFrequency, stats.Null)
	if stats.MaxFrequency > 0 {
		modeArgs := append(append([]any(nil), args...), stats.MaxFrequency)
		if err = queryOne(ctx, q, "SELECT COUNT(*) FROM ("+grouped+") AS nyadb_groups WHERE n="+s.arg(len(args)+1), modeArgs, &stats.ModeCount); err != nil {
			return stats, err
		}
		for _, frequency := range stats.Distribution {
			if frequency.Count == stats.MaxFrequency {
				stats.Modes = append(stats.Modes, frequency)
			}
		}
		if stats.Null == stats.MaxFrequency {
			stats.ModeCount++
			stats.Modes = append(stats.Modes, contract.DataAnalysisFrequency{Null: true, Count: stats.Null})
		}
	}
	return stats, nil
}

func (s *Session) DataAnalysisOperations() contract.DataAnalysisOperations {
	return contract.DataAnalysisOperations{Analyzer: s.analyzeData}
}
