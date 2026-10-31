package engines

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/nyakang/nyadb/internal/db/contract"
	"math/big"
)

// Verify is a fresh read only. It never infers that an uncertain COMMIT should
// be replayed, and cannot prove writes involving server-side generated keys.
func (s *Session) Verify(ctx context.Context, o contract.ObjectRef, edits []contract.Edit) (contract.Verification, error) {
	v := contract.Verification{Matched: true}
	meta, e := s.Structure(ctx, o)
	if e != nil {
		return v, e
	}
	positions := map[string]int{}
	for i, f := range meta.Columns {
		positions[f.Name] = i
	}
	for _, edit := range edits {
		r := contract.PageRequest{Object: o, Limit: 2}
		unknown := false
		for i, key := range meta.Key {
			var value contract.Cell
			if x, ok := edit.Values[key]; ok {
				value = x
			} else if i < len(edit.Identity.Values) {
				value = edit.Identity.Values[i]
			} else {
				unknown = true
				break
			}
			r.Filters = append(r.Filters, contract.Filter{Column: key, Operator: "=", Value: value})
		}
		if unknown || len(meta.Key) == 0 {
			v.Matched = false
			v.States = append(v.States, "unknown")
			continue
		}
		p, err := s.Page(ctx, r)
		if err != nil {
			return v, err
		}
		match := false
		if edit.Kind == "delete" {
			match = len(p.Rows) == 0
		} else if len(p.Rows) == 1 {
			match = true
			for key, value := range edit.Values {
				index, ok := positions[key]
				if !ok || !sameCell(p.Rows[0][index], value) {
					match = false
					break
				}
			}
		}
		state := "mismatch"
		if match {
			state = "matched"
		} else {
			v.Matched = false
		}
		v.States = append(v.States, state)
	}
	return v, nil
}
func sameCell(a, b contract.Cell) bool {
	if a.Kind == contract.Null || b.Kind == contract.Null {
		return a.Kind == b.Kind
	}
	if a.Kind == contract.Binary || b.Kind == contract.Binary {
		return a.Kind == b.Kind && bytes.Equal(a.Bytes, b.Bytes)
	}
	if a.Kind == contract.Decimal || a.Kind == contract.Integer || a.Kind == contract.Floating {
		ra, oa := new(big.Rat).SetString(a.Text)
		rb, ob := new(big.Rat).SetString(b.Text)
		if oa && ob {
			return ra.Cmp(rb) == 0
		}
	}
	if a.Kind == contract.JSON && b.Kind == contract.JSON {
		var x, y any
		da, db := json.NewDecoder(bytes.NewBufferString(a.Text)), json.NewDecoder(bytes.NewBufferString(b.Text))
		da.UseNumber()
		db.UseNumber()
		if da.Decode(&x) == nil && db.Decode(&y) == nil {
			xa, _ := json.Marshal(x)
			ya, _ := json.Marshal(y)
			return bytes.Equal(xa, ya)
		}
	}
	return a.Text == b.Text
}
