package schemaddl

import (
	"reflect"
	"testing"
)

func TestOpenGaussIndexDispatchUsesNativeStorageAndRejectsUnsupportedMethods(t *testing.T) {
	for _, sample := range []struct {
		name, suffix, want string
		params             []string
		defaultUstore      bool
	}{
		{"heap_default", "(id)", "btree", nil, false},
		{"heap_gin", "USING gin(tags)", "gin", nil, false},
		{"heap_spgist", "USING spgist(location)", "", nil, false},
		{"heap_ubtree", "USING ubtree(id)", "", nil, false},
		{"column_default", "(id)", "psort", []string{"orientation=column"}, false},
		{"column_alias", "USING btree(id pg_catalog.int8_ops)", "cbtree", []string{"orientation = 'COLUMN'"}, false},
		{"column_gin_alias", "USING gin(tags)", "cgin", []string{"ORIENTATION=column"}, false},
		{"column_cbtree", "USING cbtree(id)", "cbtree", []string{"orientation=column"}, false},
		{"column_hash", "USING hash(id)", "", []string{"orientation=column"}, false},
		{"column_spgist", "USING spgist(location)", "", []string{"orientation=column"}, false},
		{"ustore_default", "(id)", "ubtree", []string{"storage_type=ustore"}, false},
		{"ustore_explicit", "USING ubtree(id)", "ubtree", []string{"storage_type='USTORE'"}, false},
		{"ustore_btree", "USING btree(id)", "", []string{"storage_type=ustore"}, false},
		{"ustore_gin", "USING gin(tags)", "", []string{"storage_type=ustore"}, false},
		{"configured_ustore", "(id)", "ubtree", nil, true},
		{"explicit_heap", "(id)", "btree", []string{"storage_type=astore"}, true},
		{"misleading_literal", "((lower('USING gin')))", "psort", []string{"orientation=column"}, false},
		{"misleading_identifier", `("USING")`, "psort", []string{"orientation=column"}, false},
		{"duplicate_storage", "(id)", "", []string{"storage_type=astore", "STORAGE_TYPE=ustore"}, false},
		{"heap_is_not_native_option", "(id)", "", []string{"storage_type=heap"}, false},
		{"column_storage_conflict", "(id)", "", []string{"orientation=column", "storage_type=astore"}, false},
		{"unknown_storage", "(id)", "", []string{"storage_type=other"}, false},
		{"unknown_orientation", "(id)", "", []string{"orientation=other"}, false},
	} {
		t.Run(sample.name, func(t *testing.T) {
			table := Table{Name: "t", OpenGauss: &OpenGaussTableProperties{Parameters: sample.params}}
			d := Definition{Kind: "index", Name: "i", SQL: "CREATE INDEX i ON s.t " + sample.suffix}
			before := Signature(table)
			got, err := OpenGaussIndexProbeLayout(d, "s", table, sample.defaultUstore)
			if sample.want == "" {
				if err == nil {
					t.Fatal("unsupported dispatch authorized", got)
				}
			} else if err != nil || got.Method != sample.want || len(got.Keys) != 1 {
				t.Fatal("native dispatch differs", got, err)
			}
			if Signature(table) != before {
				t.Fatal("dispatch changed the draft")
			}
		})
	}
}

func TestOpenGaussImplicitIndexesIncludeNativeConstraintsAndDefaults(t *testing.T) {
	for _, sample := range []struct {
		params []string
		want   []string
	}{
		{nil, []string{"btree"}},
		{[]string{"orientation=column"}, []string{"psort", "cbtree"}},
		{[]string{"storage_type=ustore"}, []string{"ubtree"}},
	} {
		got, err := OpenGaussDesignImplicitIndexMethods(Table{OpenGauss: &OpenGaussTableProperties{Parameters: sample.params}}, false)
		if err != nil || !reflect.DeepEqual(got, sample.want) {
			t.Fatal("implicit native indexes differ", got, err)
		}
	}
}
