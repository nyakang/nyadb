package oracle

import (
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	goora "github.com/sijms/go-ora/v2"
	"math/big"
	"regexp"
	"strings"
)

var numberText = regexp.MustCompile(`^[+-]?(?:[0-9]+(?:\.[0-9]*)?|\.[0-9]+)(?:[eE][+-]?[0-9]{1,3})?$`)

func numberArgument(c contract.StructureColumn, text string) (any, error) {
	if len(text) > 256 || !numberText.MatchString(text) {
		return nil, connections.ErrInvalid
	}
	mantissa := strings.Split(strings.ToLower(text), "e")[0]
	digits := strings.Trim(strings.ReplaceAll(strings.TrimLeft(mantissa, "+-"), ".", ""), "0")
	if len(digits) > 38 {
		return nil, connections.ErrInvalid
	}
	n, ok := new(big.Rat).SetString(text)
	if !ok {
		return nil, connections.ErrInvalid
	}
	absolute := new(big.Rat).Abs(n)
	if absolute.Cmp(new(big.Rat).SetInt(new(big.Int).Exp(big.NewInt(10), big.NewInt(126), nil))) >= 0 {
		return nil, connections.ErrInvalid
	}
	if n.Sign() != 0 && absolute.Cmp(new(big.Rat).SetFrac(big.NewInt(1), new(big.Int).Exp(big.NewInt(10), big.NewInt(130), nil))) < 0 {
		return nil, connections.ErrInvalid
	}
	if c.NumericScale != nil {
		scale := *c.NumericScale
		if scale < -84 || scale > 127 {
			return nil, contract.ErrUnsupported
		}
		var factor *big.Rat
		if scale >= 0 {
			factor = new(big.Rat).SetInt(new(big.Int).Exp(big.NewInt(10), big.NewInt(scale), nil))
		} else {
			factor = new(big.Rat).SetFrac(big.NewInt(1), new(big.Int).Exp(big.NewInt(10), big.NewInt(-scale), nil))
		}
		scaled := new(big.Rat).Mul(n, factor)
		if scaled.Denom().Cmp(big.NewInt(1)) != 0 {
			return nil, connections.ErrInvalid
		}
		if c.NumericPrecision != nil {
			precision := *c.NumericPrecision
			if precision < 1 || precision > 38 {
				return nil, contract.ErrUnsupported
			}
			if new(big.Int).Abs(scaled.Num()).Cmp(new(big.Int).Exp(big.NewInt(10), big.NewInt(precision), nil)) >= 0 {
				return nil, connections.ErrInvalid
			}
		}
	}
	value, err := goora.NewNumberFromString(strings.TrimPrefix(strings.ToLower(text), "+"))
	if err != nil {
		return nil, connections.ErrInvalid
	}
	decoded, err := value.String()
	if err != nil {
		return nil, connections.ErrInvalid
	}
	roundtrip, ok := new(big.Rat).SetString(decoded)
	if !ok || roundtrip.Cmp(n) != 0 {
		return nil, connections.ErrInvalid
	}
	return value, nil
}
