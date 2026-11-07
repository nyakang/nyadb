package schemaddl

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

// PlanOpenGaussRename changes only a qualified table name. It neither creates
// an independent declaration nor executes defaults, row expressions or data.
func PlanOpenGaussRename(original Snapshot, destination string) (string, error) {
	p := original.OpenGauss
	if original.Engine != "opengauss" || !original.Exists || p == nil || p.RelationOID == 0 || p.Name != original.Table.Name {
		return "", ErrConflict
	}
	if revision, err := p.Fingerprint(); err != nil || revision != original.Revision {
		return "", errors.Join(err, ErrConflict)
	}
	if PostgreSQLIdentifier(destination) != nil || destination == p.Name {
		return "", errors.New("a different valid table name is required")
	}
	if !p.CanManage || p.Hooks {
		return "", errors.New("table rename requires management permission and no enabled DDL hooks")
	}
	for _, entry := range p.Entries {
		if entry.Kind != "relation" || entry.OID != p.RelationOID {
			continue
		}
		var row struct {
			Name        string   `json:"relname"`
			Kind        string   `json:"relkind"`
			Persistence string   `json:"relpersistence"`
			OfType      uint64   `json:"reloftype"`
			Options     []string `json:"reloptions"`
		}
		if json.Unmarshal(entry.Data, &row) != nil || row.Name != p.Name || row.Kind != "r" || row.OfType != 0 || row.Persistence != "p" && row.Persistence != "u" {
			return "", ErrUnsupported
		}
		for _, option := range row.Options {
			option = strings.ToLower(strings.ReplaceAll(option, " ", ""))
			if option == "blockchain=true" || option == "orientation=timeseries" {
				return "", ErrUnsupported
			}
		}
		return "ALTER TABLE " + Quote(p.Namespace) + "." + Quote(p.Name) + " RENAME TO " + Quote(destination), nil
	}
	return "", ErrConflict
}

// OpenGaussRenameArrayName follows native makeArrayTypeName/makeObjectName.
// Native collisions use numeric suffixes; UTF-8 is clipped by bytes. The
// executor captures occupied names before DDL and rejects a changed outcome.
func OpenGaussRenameArrayName(destination string, occupied map[string]bool) (string, error) {
	if PostgreSQLIdentifier(destination) != nil {
		return "", ErrConflict
	}
	for pass := 0; pass <= len(occupied); pass++ {
		suffix := ""
		if pass != 0 {
			suffix = "_" + strconv.Itoa(pass)
		}
		length := min(len(destination), 62-len(suffix))
		for length > 0 && !utf8.ValidString(destination[:length]) {
			length--
		}
		name := "_" + destination[:length] + suffix
		if !occupied[name] {
			return name, nil
		}
	}
	return "", ErrConflict
}

// VerifyOpenGaussRename retains all numeric identities and complete native
// records, including unknown properties, dependencies and physical files.
// Only primary relation/type names and the partition-root name may change.
// Presentation rows are deparsed from those retained native records, and must
// retain their known shape; they do not grant permission to change a catalog.
func VerifyOpenGaussRename(original Snapshot, destination, arrayName string, actual Snapshot) error {
	if _, err := PlanOpenGaussRename(original, destination); err != nil {
		return err
	}
	if actual.Engine != "opengauss" || !actual.Exists || actual.Table.Name != destination || actual.OpenGauss == nil || actual.OpenGauss.Name != destination {
		return ErrConflict
	}
	if revision, err := actual.OpenGauss.Fingerprint(); err != nil || revision != actual.Revision {
		return errors.Join(err, ErrConflict)
	}
	expected := original.OpenGauss.Clone()
	expected.Name = destination
	var rowType, arrayType uint64
	for _, entry := range expected.Entries {
		if entry.Kind == "relation" && entry.OID == expected.RelationOID {
			var row struct {
				Type uint64 `json:"reltype"`
			}
			if json.Unmarshal(entry.Data, &row) != nil || row.Type == 0 {
				return ErrConflict
			}
			rowType = row.Type
		}
	}
	for _, entry := range expected.Entries {
		if entry.Kind == "type" && entry.OID == rowType {
			var row struct {
				Array uint64 `json:"typarray"`
			}
			if json.Unmarshal(entry.Data, &row) != nil {
				return ErrConflict
			}
			arrayType = row.Array
		}
	}
	if rowType == 0 || arrayType != 0 && PostgreSQLIdentifier(arrayName) != nil {
		return ErrConflict
	}
	for i, entry := range expected.Entries {
		field, name := "", ""
		if entry.Kind == "relation" && entry.OID == expected.RelationOID {
			field, name = "relname", destination
		}
		if entry.Kind == "type" && entry.OID == rowType {
			field, name = "typname", destination
		}
		if entry.Kind == "type" && arrayType != 0 && entry.OID == arrayType {
			field, name = "typname", arrayName
		}
		if entry.Kind == "partition" {
			var row struct {
				Parent uint64 `json:"parentid"`
				Kind   string `json:"parttype"`
			}
			if json.Unmarshal(entry.Data, &row) != nil {
				return ErrConflict
			}
			if row.Parent == expected.RelationOID && row.Kind == "r" {
				field, name = "relname", destination
			}
		}
		if field != "" {
			var row map[string]json.RawMessage
			if json.Unmarshal(entry.Data, &row) != nil || row[field] == nil {
				return ErrConflict
			}
			row[field], _ = json.Marshal(name)
			data, err := json.Marshal(row)
			if err != nil {
				return err
			}
			expected.Entries[i].Data = data
		}
	}
	want, files, err := openGaussRenameRecords(expected)
	if err != nil {
		return err
	}
	got, afterFiles, err := openGaussRenameRecords(actual.OpenGauss)
	if err != nil || want != got || Signature(files) != Signature(afterFiles) {
		return errors.Join(err, ErrConflict)
	}
	return nil
}

func openGaussRenameRecords(catalog *OpenGaussCatalog) (string, map[string]json.RawMessage, error) {
	p := catalog.Clone()
	p.Entries = nil
	files := map[string]json.RawMessage{}
	for _, entry := range catalog.Entries {
		if entry.Kind == "copy_definition" || entry.Kind == "design_definition" {
			decoder := json.NewDecoder(bytes.NewReader(entry.Data))
			decoder.DisallowUnknownFields()
			var err error
			if entry.Kind == "copy_definition" {
				var definition OpenGaussCopyDefinition
				err = decoder.Decode(&definition)
			} else {
				var definition struct{ DDL string }
				err = decoder.Decode(&definition)
			}
			if err != nil || entry.OID != catalog.RelationOID {
				return "", nil, errors.Join(err, ErrConflict)
			}
			continue
		}
		if entry.Kind == "relation" || entry.Kind == "partition" {
			var row map[string]json.RawMessage
			if json.Unmarshal(entry.Data, &row) != nil || row["relfilenode"] == nil {
				return "", nil, ErrConflict
			}
			files[fmt.Sprintf("%s:%d:%d", entry.Kind, entry.OID, entry.SubID)] = row["relfilenode"]
		}
		p.Entries = append(p.Entries, entry)
	}
	fingerprint, err := p.Fingerprint()
	return fingerprint, files, err
}
