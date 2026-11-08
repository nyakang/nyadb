package qdrant

import (
	"bytes"
	"encoding/json"
	"math"
	"math/big"
	"slices"
	"strconv"
	"strings"

	"github.com/nyakang/nyadb/internal/db/contract"
)

type vectorWriteSpec struct {
	size               int
	distance, datatype string
	multi, sparse      bool
}
type writeVector struct {
	rows    [][]float32
	indices []uint32
	sparse  bool
}

func vectorWriteSchema(body []byte) (map[string]vectorWriteSpec, error) {
	raw, err := NativeResult(body)
	info, e := object(raw)
	config, e2 := object(info["config"])
	params, e3 := object(config["params"])
	if err != nil || e != nil || e2 != nil || e3 != nil {
		return nil, ErrProtocol
	}
	specs := map[string]vectorWriteSpec{}
	if v := params["vectors"]; len(v) > 0 && !bytes.Equal(v, []byte("null")) {
		fields, err := object(v)
		if err != nil {
			return nil, err
		}
		if fields["size"] != nil {
			fields = map[string]json.RawMessage{"": v}
		}
		for name, raw := range fields {
			f, err := object(raw)
			var spec vectorWriteSpec
			if err != nil || decodeJSON(f["size"], &spec.size) != nil || spec.size < 1 || decodeJSON(f["distance"], &spec.distance) != nil {
				return nil, ErrProtocol
			}
			spec.datatype = "float32"
			if dt := f["datatype"]; len(dt) > 0 && !bytes.Equal(dt, []byte("null")) {
				if decodeJSON(dt, &spec.datatype) != nil {
					return nil, ErrProtocol
				}
			}
			if spec.distance != "Cosine" && spec.distance != "Dot" && spec.distance != "Euclid" && spec.distance != "Manhattan" {
				return nil, contract.ErrUnsupported
			}
			if spec.datatype != "float32" && spec.datatype != "float16" && spec.datatype != "uint8" {
				return nil, contract.ErrUnsupported
			}
			if m := f["multivector_config"]; len(m) > 0 && !bytes.Equal(m, []byte("null")) {
				mc, err := object(m)
				var comparator string
				if err != nil || decodeJSON(mc["comparator"], &comparator) != nil || comparator != "max_sim" {
					return nil, contract.ErrUnsupported
				}
				spec.multi = true
			}
			specs[name] = spec
		}
	}
	if raw := params["sparse_vectors"]; len(raw) > 0 && !bytes.Equal(raw, []byte("null")) {
		fields, err := object(raw)
		if err != nil {
			return nil, err
		}
		for name := range fields {
			if _, exists := specs[name]; exists {
				return nil, ErrProtocol
			}
			// Sparse index datatype affects the inverted index, not original point
			// values returned by the sparse vector storage.
			specs[name] = vectorWriteSpec{sparse: true, datatype: "float32"}
		}
	}
	return specs, nil
}

func float32Array(raw []byte) ([]float32, error) {
	var numbers []json.RawMessage
	if decodeJSON(raw, &numbers) != nil || numbers == nil {
		return nil, ErrInvalid
	}
	values := make([]float32, len(numbers))
	for i, n := range numbers {
		v, err := strconv.ParseFloat(string(n), 32)
		if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
			return nil, ErrInvalid
		}
		values[i] = float32(v)
	}
	return values, nil
}

// halfValue rounds an already rounded native float32 to IEEE binary16 using
// ties-to-even. It does not change any request bytes.
func halfValue(v float32) (float32, error) {
	if v == 0 {
		return v, nil
	}
	a := math.Abs(float64(v))
	_, exponent := math.Frexp(a)
	step := math.Ldexp(1, max(-24, exponent-11))
	out := math.RoundToEven(a/step) * step
	if out > 65504 {
		return 0, ErrBounds
	}
	return float32(math.Copysign(out, float64(v))), nil
}

func prepareDense(values []float32, spec vectorWriteSpec) ([]float32, error) {
	if len(values) != spec.size {
		return nil, ErrInvalid
	}
	if spec.distance == "Cosine" && spec.datatype != "uint8" {
		var length float64
		for _, v := range values {
			length += float64(v * v)
		}
		if length >= 1.1920928955078125e-7 && math.Abs(length-1) > 1e-6 {
			norm := float32(math.Sqrt(length))
			if math.IsInf(float64(norm), 0) || norm == 0 {
				return nil, ErrBounds
			}
			for i := range values {
				values[i] /= norm
			}
		}
	}
	for i, v := range values {
		switch spec.datatype {
		case "uint8":
			values[i] = float32(math.Trunc(min(255, max(0, float64(v)))))
		case "float16":
			r, err := halfValue(v)
			if err != nil {
				return nil, err
			}
			values[i] = r
		}
	}
	return values, nil
}

func decodeWriteVectors(raw []byte, specs map[string]vectorWriteSpec, prepare bool) (map[string]writeVector, error) {
	raw = bytes.TrimSpace(raw)
	if !prepare && (len(raw) == 0 || bytes.Equal(raw, []byte("null"))) {
		return map[string]writeVector{}, nil
	}
	var fields map[string]json.RawMessage
	if len(raw) > 0 && raw[0] == '[' {
		fields = map[string]json.RawMessage{"": bytes.Clone(raw)}
	} else {
		var err error
		fields, err = object(raw)
		if err != nil {
			return nil, ErrInvalid
		}
	}
	vectors := map[string]writeVector{}
	for name, raw := range fields {
		spec, ok := specs[name]
		if !ok {
			return nil, contract.ErrUnsupported
		}
		v := writeVector{sparse: spec.sparse}
		if spec.sparse {
			f, err := object(raw)
			if err != nil || len(f) != 2 || decodeJSON(f["indices"], &v.indices) != nil || v.indices == nil {
				return nil, ErrInvalid
			}
			values, err := float32Array(f["values"])
			if err != nil || len(values) != len(v.indices) {
				return nil, ErrInvalid
			}
			order := make([]int, len(values))
			for i := range order {
				order[i] = i
			}
			slices.SortFunc(order, func(a, b int) int {
				if v.indices[a] < v.indices[b] {
					return -1
				}
				if v.indices[a] > v.indices[b] {
					return 1
				}
				return 0
			})
			indices, sorted := make([]uint32, len(order)), make([]float32, len(order))
			for i, j := range order {
				indices[i], sorted[i] = v.indices[j], values[j]
				if i > 0 && indices[i] == indices[i-1] {
					return nil, ErrInvalid
				}
			}
			v.indices, v.rows = indices, [][]float32{sorted}
		} else {
			var elements []json.RawMessage
			if decodeJSON(raw, &elements) != nil || len(elements) == 0 {
				return nil, ErrInvalid
			}
			rows := []json.RawMessage{raw}
			if bytes.HasPrefix(bytes.TrimSpace(elements[0]), []byte("[")) {
				if !spec.multi {
					return nil, ErrInvalid
				}
				rows = elements
			}
			for _, row := range rows {
				values, err := float32Array(row)
				if err != nil || len(values) != spec.size {
					return nil, ErrInvalid
				}
				if prepare {
					values, err = prepareDense(values, spec)
					if err != nil {
						return nil, err
					}
				}
				v.rows = append(v.rows, values)
			}
		}
		vectors[name] = v
	}
	return vectors, nil
}

func vectorsMatch(expected, actual map[string]writeVector, specs map[string]vectorWriteSpec) bool {
	if len(expected) != len(actual) {
		return false
	}
	for name, a := range expected {
		b, ok := actual[name]
		if !ok || a.sparse != b.sparse || !slices.Equal(a.indices, b.indices) || len(a.rows) != len(b.rows) {
			return false
		}
		for i, row := range a.rows {
			if len(row) != len(b.rows[i]) {
				return false
			}
			for j, v := range row {
				other := b.rows[i][j]
				if v == other {
					continue
				}
				spec := specs[name]
				// SIMD reduction order can slightly change Cosine normalization.
				// Other metrics and original sparse values compare at native precision.
				if spec.sparse || spec.distance != "Cosine" || spec.datatype != "float32" || math.Abs(float64(v-other)) > 1e-6+math.Abs(float64(v))*1e-5 {
					return false
				}
			}
		}
	}
	return true
}

func payloadEqual(expected, actual []byte) bool {
	if len(expected) == 0 || bytes.Equal(bytes.TrimSpace(expected), []byte("null")) {
		expected = []byte("{}")
	}
	if len(actual) == 0 || bytes.Equal(bytes.TrimSpace(actual), []byte("null")) {
		actual = []byte("{}")
	}
	var a, b any
	if decodeJSON(expected, &a) != nil || decodeJSON(actual, &b) != nil {
		return false
	}
	var same func(any, any) bool
	same = func(a, b any) bool {
		switch a := a.(type) {
		case map[string]any:
			m, ok := b.(map[string]any)
			if !ok || len(a) != len(m) {
				return false
			}
			for k, v := range a {
				w, ok := m[k]
				if !ok || !same(v, w) {
					return false
				}
			}
			return true
		case []any:
			v, ok := b.([]any)
			if !ok || len(a) != len(v) {
				return false
			}
			for i := range a {
				if !same(a[i], v[i]) {
					return false
				}
			}
			return true
		case json.Number:
			n, ok := b.(json.Number)
			if !ok {
				return false
			}
			r1, ok1 := boundedJSONRat(a)
			r2, ok2 := boundedJSONRat(n)
			if ok1 && ok2 && r1.Cmp(r2) == 0 {
				return true
			}
			if !strings.ContainsAny(string(a), ".eE") {
				return false
			}
			f1, e1 := strconv.ParseFloat(string(a), 64)
			f2, e2 := strconv.ParseFloat(string(n), 64)
			return e1 == nil && e2 == nil && !math.IsInf(f1, 0) && !math.IsInf(f2, 0) && f1 == f2
		default:
			return a == b
		}
	}
	return same(a, b)
}

// Rat exponentiation must never follow an unbounded user-provided exponent.
func boundedJSONRat(n json.Number) (*big.Rat, bool) {
	s := string(n)
	if len(s) > 1024 {
		return nil, false
	}
	if i := strings.IndexAny(s, "eE"); i >= 0 {
		e, err := strconv.Atoi(s[i+1:])
		if err != nil || e < -400 || e > 400 {
			return nil, false
		}
	}
	return new(big.Rat).SetString(s)
}

func validWritePayload(raw []byte) bool {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return true
	}
	var value any
	if decodeJSON(raw, &value) != nil {
		return false
	}
	var valid func(any) bool
	valid = func(value any) bool {
		switch v := value.(type) {
		case map[string]any:
			for _, x := range v {
				if !valid(x) {
					return false
				}
			}
		case []any:
			for _, x := range v {
				if !valid(x) {
					return false
				}
			}
		case json.Number:
			s := string(v)
			if strings.ContainsAny(s, ".eE") {
				f, e := strconv.ParseFloat(s, 64)
				return e == nil && !math.IsInf(f, 0) && !math.IsNaN(f)
			}
			if strings.HasPrefix(s, "-") {
				_, e := strconv.ParseInt(s, 10, 64)
				return e == nil
			}
			_, e := strconv.ParseUint(s, 10, 64)
			return e == nil
		}
		return true
	}
	return valid(value)
}
