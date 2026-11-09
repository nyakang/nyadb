package graphschema

import "github.com/nyakang/nyadb/internal/db/contract"

// Bound the complete value tree before marshaling, converting or cloning it.
// This also rejects graph entities/cycles, which cannot be SHOW schema fields.
type budget struct{ remaining int }

func (b *budget) value(v contract.GraphValue, depth int) error {
	if depth > 16 || len(v.Values) > 1000 || len(v.Properties) > 1000 {
		return ErrBounds
	}
	if v.Node != nil || v.Relationship != nil || v.Path != nil {
		return ErrInvalid
	}
	b.remaining -= len(v.Kind) + len(v.Text) + 64
	if b.remaining < 0 {
		return ErrBounds
	}
	for _, p := range v.Properties {
		b.remaining -= len(p.Name) + 32
		if b.remaining < 0 {
			return ErrBounds
		}
		if e := b.value(p.Value, depth+1); e != nil {
			return e
		}
	}
	for _, item := range v.Values {
		if e := b.value(item, depth+1); e != nil {
			return e
		}
	}
	return nil
}
func catalogBounds(c contract.GraphSchemaCatalog) error {
	b := budget{remaining: MaxBytes}
	for _, r := range []contract.GraphResult{c.Indexes, c.Constraints} {
		if len(r.Columns) > 128 {
			return ErrBounds
		}
		for _, column := range r.Columns {
			b.remaining -= len(column) + 16
		}
		for _, row := range r.Rows {
			if len(row) != len(r.Columns) {
				return ErrIncomplete
			}
			for _, value := range row {
				if e := b.value(value, 0); e != nil {
					return e
				}
			}
		}
	}
	if b.remaining < 0 {
		return ErrBounds
	}
	return nil
}
