package engines

import (
	"database/sql/driver"
	"encoding/json"
	"errors"
	"math"
	"math/big"
	"strings"
	"unicode/utf8"

	sqlitedriver "modernc.org/sqlite"
)

const sqliteNumericFunction = "_nyadb_numeric_stats_v1"

// Each user-session driver owns its functions. Configuration storage and
// unrelated SQLite handles never receive these registrations.
func newSQLiteDataDriver() (*sqlitedriver.Driver, error) {
	d := &sqlitedriver.Driver{}
	registerText := func(name string, args int32, fn func(*sqlitedriver.FunctionContext, []driver.Value) (driver.Value, error)) error {
		return d.RegisterFunction(name, &sqlitedriver.FunctionImpl{NArgs: args, Deterministic: true, VolatileArgs: true, Scalar: fn})
	}
	if err := d.RegisterFunction(sqliteNumericFunction, &sqlitedriver.FunctionImpl{NArgs: 1, Deterministic: true, VolatileArgs: true, MakeAggregate: func(sqlitedriver.FunctionContext) (sqlitedriver.AggregateFunction, error) {
		return &sqliteNumeric{}, nil
	}}); err != nil {
		return nil, err
	}
	if err := registerText("_nyadb_text_units_v1", 1, func(_ *sqlitedriver.FunctionContext, args []driver.Value) (driver.Value, error) {
		if args[0] == nil {
			return nil, nil
		}
		text, ok := args[0].(string)
		if !ok || !utf8.ValidString(text) {
			return nil, errors.New("invalid UTF-8 field")
		}
		return int64(utf8.RuneCountInString(text)), nil
	}); err != nil {
		return nil, err
	}
	if err := registerText("_nyadb_text_slice_v1", 3, func(_ *sqlitedriver.FunctionContext, args []driver.Value) (driver.Value, error) {
		if args[0] == nil {
			return nil, nil
		}
		text, ok := args[0].(string)
		offset, aok := args[1].(int64)
		units, bok := args[2].(int64)
		if !ok || !aok || !bok || offset < 1 || units < 1 || units > (1<<20)+1 || !utf8.ValidString(text) {
			return nil, errors.New("invalid bounded text fragment")
		}
		start, end := len(text), len(text)
		n := int64(1)
		for at := range text {
			if n == offset {
				start = at
			}
			if n == offset+units {
				end = at
				break
			}
			n++
		}
		return strings.Clone(text[start:end]), nil
	}); err != nil {
		return nil, err
	}
	return d, nil
}

type numericSummary struct {
	Count, Zero               int64
	Min, Max, Mean, Deviation string
}
type sqliteNumeric struct {
	count, zero  int64
	sum, squares big.Rat
	min, max     *big.Rat
}

func numericRat(v driver.Value) (*big.Rat, error) {
	switch n := v.(type) {
	case int64:
		return new(big.Rat).SetInt64(n), nil
	case float64:
		if math.IsInf(n, 0) || math.IsNaN(n) {
			return nil, errors.New("non-finite numeric statistics are unsupported")
		}
		return new(big.Rat).SetFloat64(n), nil
	}
	return nil, nil
}
func (a *sqliteNumeric) Step(_ *sqlitedriver.FunctionContext, args []driver.Value) error {
	v, err := numericRat(args[0])
	if err != nil || v == nil {
		return err
	}
	a.count++
	if v.Sign() == 0 {
		a.zero++
	}
	a.sum.Add(&a.sum, v)
	a.squares.Add(&a.squares, new(big.Rat).Mul(v, v))
	if a.min == nil || v.Cmp(a.min) < 0 {
		a.min = new(big.Rat).Set(v)
	}
	if a.max == nil || v.Cmp(a.max) > 0 {
		a.max = new(big.Rat).Set(v)
	}
	return nil
}
func (*sqliteNumeric) WindowInverse(*sqlitedriver.FunctionContext, []driver.Value) error {
	return errors.New("numeric summary is not a sliding-window function")
}
func ratDisplay(v *big.Rat) string {
	if v.IsInt() {
		return v.Num().String()
	}
	// Preserve terminating decimal and binary fractions exactly. Only repeating
	// quotients need a rounded presentation (32 fractional decimal places).
	denominator := new(big.Int).Set(v.Denom())
	two, five := big.NewInt(2), big.NewInt(5)
	remainder := new(big.Int)
	powers := [2]int{}
	for i, factor := range []*big.Int{two, five} {
		for {
			quotient := new(big.Int)
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
	places := 32
	if denominator.Cmp(big.NewInt(1)) == 0 {
		places = max(powers[0], powers[1])
		if places > 4096 {
			places = 32
		}
	}
	return strings.TrimRight(strings.TrimRight(v.FloatString(places), "0"), ".")
}
func (a *sqliteNumeric) WindowValue(*sqlitedriver.FunctionContext) (driver.Value, error) {
	out := numericSummary{Count: a.count, Zero: a.zero}
	if a.count > 0 {
		n := new(big.Rat).SetInt64(a.count)
		mean := new(big.Rat).Quo(&a.sum, n)
		variance := new(big.Rat).Sub(new(big.Rat).Quo(&a.squares, n), new(big.Rat).Mul(mean, mean))
		if variance.Sign() < 0 {
			return nil, errors.New("invalid numeric variance")
		}
		f := new(big.Float).SetPrec(256).SetRat(variance)
		f.Sqrt(f)
		out.Min, out.Max, out.Mean, out.Deviation = ratDisplay(a.min), ratDisplay(a.max), ratDisplay(mean), f.Text('g', 32)
	}
	b, err := json.Marshal(out)
	return string(b), err
}
func (*sqliteNumeric) Final(*sqlitedriver.FunctionContext) {}
