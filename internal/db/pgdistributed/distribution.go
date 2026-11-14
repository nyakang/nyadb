// Package pgdistributed preserves native PostgreSQL-family distribution rules
// independently of connection configuration, drivers and UI presentation.
package pgdistributed

import (
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/nyakang/nyadb/internal/db/contract"
)

type Product string

const (
	Cloudberry  Product = "cloudberry"
	OpenTenBase Product = "opentenbase"
)

// Policy retains catalog column order and identifiers exactly. A policy is
// read-only metadata; rendering it never authorizes a table mutation.
type Policy struct {
	Product Product
	Code    string
	Columns []string
}

var ErrPolicy = errors.New("invalid native distribution policy")

func (p Policy) Clause() (string, error) {
	var prefix, mode string
	var keyed bool
	switch p.Product {
	case Cloudberry:
		prefix = "DISTRIBUTED "
		switch p.Code {
		case "p":
			if len(p.Columns) == 0 {
				mode = "RANDOMLY"
			} else {
				mode, keyed = "BY", true
			}
		case "r":
			mode = "REPLICATED"
		default:
			return "", contract.ErrUnsupported
		}
	case OpenTenBase:
		prefix = "DISTRIBUTE BY "
		switch p.Code {
		case "H":
			mode, keyed = "HASH", true
		case "M":
			mode, keyed = "MODULO", true
		case "S":
			mode, keyed = "SHARD", true
		case "R":
			mode = "REPLICATION"
		case "N":
			mode = "ROUNDROBIN"
		default:
			return "", contract.ErrUnsupported
		}
	default:
		return "", contract.ErrUnsupported
	}
	if !keyed {
		if len(p.Columns) != 0 {
			return "", ErrPolicy
		}
		return prefix + mode, nil
	}
	if len(p.Columns) == 0 || len(p.Columns) > 128 {
		return "", ErrPolicy
	}
	seen := make(map[string]bool, len(p.Columns))
	quoted := make([]string, len(p.Columns))
	used := 0
	for i, column := range p.Columns {
		used += len(column)
		if column == "" || len(column) > 1024 || used > 128<<10 || !utf8.ValidString(column) || strings.ContainsRune(column, 0) || seen[column] {
			return "", ErrPolicy
		}
		seen[column] = true
		quoted[i] = `"` + strings.ReplaceAll(column, `"`, `""`) + `"`
	}
	return prefix + mode + " (" + strings.Join(quoted, ", ") + ")", nil
}
