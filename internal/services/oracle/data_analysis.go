package oracle

import (
	"context"
	"database/sql"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func (s *Session) DataAnalysisOperations() contract.DataAnalysisOperations {
	return contract.DataAnalysisOperations{Analyzer: s.analyze}
}
func (s *Session) analyze(ctx context.Context, id int64, schema, name string, o contract.DataOptions, columns []string) (*contract.DataAnalysis, error) {
	child, done, err := s.request(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	t, err := s.resolve(child, s.db, id, schema, name, o.Object)
	if err != nil {
		return nil, err
	}
	if len(columns) == 0 {
		for _, c := range t.Columns {
			columns = append(columns, c.Name)
		}
	}
	query, args, err := dataQuery(t, o, nil, false)
	if err != nil {
		return nil, err
	}
	from := " FROM (" + query + ") nyadb_analysis"
	tx, err := s.db.BeginTx(child, &sql.TxOptions{Isolation: sql.LevelDefault})
	if err != nil {
		return nil, safeError(err)
	}
	defer tx.Rollback()
	result := &contract.DataAnalysis{}
	start := time.Now()
	for _, name := range columns {
		var col *contract.StructureColumn
		for i := range t.Columns {
			if t.Columns[i].Name == name {
				col = &t.Columns[i]
			}
		}
		if col == nil || !comparable(*col) || col.ValueKind == contract.Binary {
			return nil, contract.ErrUnsupported
		}
		c := quote(name)
		aggregate := c
		statement := "SELECT COUNT(*),COUNT(*)-COUNT(" + c + "),COUNT(DISTINCT " + c + "),MIN(" + aggregate + "),MAX(" + aggregate + ")" + from
		rows, err := tx.QueryContext(child, statement, args...)
		if err != nil {
			return nil, safeError(err)
		}
		v := contract.DataColumnAnalysis{Name: name, Kind: col.DataType}
		var min, max any
		if !rows.Next() {
			rows.Close()
			return nil, contract.ErrUnsupported
		}
		err = rows.Scan(&v.Rows, &v.Null, &v.Distinct, &min, &max)
		rows.Close()
		if err != nil {
			return nil, safeError(err)
		}
		v.Min = previewValue(min, col.DataType, 1024).Display()
		v.Max = previewValue(max, col.DataType, 1024).Display()
		if col.Collation != nil {
			v.Collation = *col.Collation
		}
		if col.ValueKind == contract.Decimal || col.ValueKind == contract.Integer || col.ValueKind == contract.Floating {
			rows, err = tx.QueryContext(child, "SELECT COUNT("+c+"),COALESCE(SUM(CASE WHEN "+c+"=0 THEN 1 ELSE 0 END),0),AVG("+averageExpression(*col, c)+"),STDDEV("+c+")"+from, args...)
			if err != nil {
				return nil, safeError(err)
			}
			var mean, std any
			if !rows.Next() {
				rows.Close()
				return nil, contract.ErrUnsupported
			}
			err = rows.Scan(&v.Numeric, &v.Zero, &mean, &std)
			rows.Close()
			if err != nil {
				return nil, safeError(err)
			}
			v.Mean = cell(mean, "NUMBER").Display()
			v.StdDeviation = cell(std, "NUMBER").Display()
		}
		rows, err = tx.QueryContext(child, "SELECT "+c+",COUNT(*) AS n"+from+" GROUP BY "+c+" ORDER BY n DESC,"+c+" FETCH FIRST 5 ROWS ONLY", args...)
		if err != nil {
			return nil, safeError(err)
		}
		for rows.Next() {
			var raw any
			var n int64
			if err = rows.Scan(&raw, &n); err != nil {
				rows.Close()
				return nil, safeError(err)
			}
			value := previewValue(raw, col.DataType, 1024)
			frequency := contract.DataAnalysisFrequency{Value: value.Display(), Count: n, Null: value.Kind == contract.Null, Truncated: value.Truncated}
			v.Distribution = append(v.Distribution, frequency)
			if n > v.MaxFrequency {
				v.MaxFrequency = n
			}
			if n == v.MaxFrequency {
				v.Modes = append(v.Modes, frequency)
				v.ModeCount++
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, safeError(err)
		}
		grouped := "SELECT COUNT(*) AS n" + from + " WHERE " + c + " IS NOT NULL GROUP BY " + c
		rows, err = tx.QueryContext(child, "SELECT COALESCE(MAX(n),0),COALESCE(SUM(CASE WHEN n=1 THEN 1 ELSE 0 END),0) FROM ("+grouped+") nyadb_frequencies", args...)
		if err != nil {
			return nil, safeError(err)
		}
		if !rows.Next() {
			rows.Close()
			return nil, contract.ErrUnsupported
		}
		err = rows.Scan(&v.MaxFrequency, &v.Singletons)
		rows.Close()
		if err != nil {
			return nil, safeError(err)
		}
		rows, err = tx.QueryContext(child, "SELECT COUNT(*) FROM ("+grouped+") nyadb_modes WHERE n=(SELECT COALESCE(MAX(n),0) FROM ("+grouped+") nyadb_max)", args...)
		if err != nil {
			return nil, safeError(err)
		}
		if !rows.Next() {
			rows.Close()
			return nil, contract.ErrUnsupported
		}
		err = rows.Scan(&v.ModeCount)
		rows.Close()
		if err != nil {
			return nil, safeError(err)
		}
		v.Modes = nil
		for _, frequency := range v.Distribution {
			if !frequency.Null && frequency.Count == v.MaxFrequency {
				v.Modes = append(v.Modes, frequency)
			}
		}
		result.Rows = v.Rows
		result.Columns = append(result.Columns, v)
	}
	result.Duration = time.Since(start)
	result.CompletedAt = time.Now()
	return result, nil
}

func averageExpression(col contract.StructureColumn, expression string) string {
	if col.ValueKind == contract.Integer {
		return expression
	}
	return expression
}
