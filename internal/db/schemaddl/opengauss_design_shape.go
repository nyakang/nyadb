package schemaddl

import (
	"errors"
	"sort"
	"strings"
)

// Bind native normalized declarations to the explicitly desired names/IDs.
// Catalog-only identity metadata is checked separately by the graph verifier.
func OpenGaussDesignDeclarationShape(table Table, names OpenGaussDesignNames, sequences []OpenGaussCopySequence) (string, error) {
	t := table.Clone()
	if t.OpenGauss == nil {
		return "", ErrConflict
	}
	for i, c := range t.Clauses {
		if c.Kind == "column" {
			id := names.Columns[c.Name]
			if id == "" {
				return "", ErrConflict
			}
			t.Clauses[i].ID = id
			f, err := ParseOpenGaussField(c.SQL)
			if err != nil {
				return "", err
			}
			for _, seq := range sequences {
				if c.OpenGauss != nil && seq.Column == c.OpenGauss.Number {
					if _, err := gaussCopyDefault(OpenGaussCopyColumn{Default: f.Default, Generated: f.Generation}, seq, seq.Schema, seq.Name); err == nil {
						f.Default = "owned_sequence:" + id
					}
				}
			}
			f.Type = gaussCopySQLSignature(f.Type)
			if f.Collation != "" {
				f.Collation = gaussCopySQLSignature(f.Collation)
			}
			if f.HasDefault && !strings.HasPrefix(f.Default, "owned_sequence:") {
				f.Default = gaussCopySQLSignature(f.Default)
			}
			if f.Generation != "" {
				f.Generation = gaussCopySQLSignature(f.Generation)
			}
			t.Clauses[i].SQL = Signature(f)
		} else {
			id := names.Constraints[c.Name]
			if id == "" {
				return "", ErrConflict
			}
			t.Clauses[i].ID = id
			t.Clauses[i].SQL = gaussCopySQLSignature(c.SQL)
		}
		if c.OpenGauss != nil {
			t.Clauses[i].OpenGauss.Number, t.Clauses[i].OpenGauss.OID = 0, 0
			if c.OpenGauss.Comment == nil {
				t.Clauses[i].OpenGauss = nil
			}
		}
	}
	for i, d := range t.Definitions {
		if names.Definitions[d.Kind+":"+d.Name] == "" {
			return "", errors.New("unbound native definition")
		}
		t.Definitions[i].SQL = gaussCopySQLSignature(d.SQL)
		if d.OpenGauss != nil {
			t.Definitions[i].OpenGauss.OID = 0
			if d.Kind != "index" && d.OpenGauss.Enabled == "" {
				t.Definitions[i].OpenGauss.Enabled = "O"
			}
			if d.OpenGauss.Comment == nil && (d.Kind == "index" || d.OpenGauss.Enabled == "O") {
				t.Definitions[i].OpenGauss = nil
			}
		}
	}
	sort.Slice(t.Clauses, func(i, j int) bool { return t.Clauses[i].ID < t.Clauses[j].ID })
	sort.Slice(t.Definitions, func(i, j int) bool {
		return t.Definitions[i].Kind+":"+t.Definitions[i].Name < t.Definitions[j].Kind+":"+t.Definitions[j].Name
	})
	sort.Strings(t.OpenGauss.Parameters)
	return Signature(t), nil
}
