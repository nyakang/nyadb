package engines

import (
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"

	"github.com/nyakang/nyadb/internal/db/schemaddl"
)

// These private seed parameters identify originally captured objects. Catalog
// names come only from this fixed map; no label, SQL, owner or data value is
// interpolated into the catalog query. Subordinate rows seed their owning
// catalog object, while dependencies retain their explicit catalog class.
type postgresOriginalSeed struct {
	Catalog  string `json:"catalog,omitempty"`
	ClassOID int64  `json:"classid,omitempty"`
	OID      int64  `json:"objid"`
}

func postgresOriginalCatalogSeeds(original *schemaddl.PostgreSQLCatalog) (string, error) {
	if original == nil {
		return "[]", nil
	}
	if _, err := original.Fingerprint(); err != nil {
		return "", err
	}
	seeds := map[postgresOriginalSeed]bool{}
	add := func(seed postgresOriginalSeed) error {
		if seed.OID <= 0 || seed.OID > 1<<32-1 || seed.ClassOID < 0 || seed.ClassOID > 1<<32-1 || seed.Catalog == "" && seed.ClassOID == 0 {
			return errors.New("invalid original PostgreSQL catalog seed identity")
		}
		seeds[seed] = true
		if len(seeds) > 10000 {
			return errors.New("original PostgreSQL catalog seeds exceed record limit")
		}
		return nil
	}
	for _, entry := range original.Entries {
		if entry.Kind == "publication_namespace" && original.ServerVersion < 150000 {
			return "", errors.New("original PostgreSQL catalog seed kind is unavailable in this server version")
		}
		var fields map[string]json.RawMessage
		if json.Unmarshal(entry.Data, &fields) != nil || fields == nil {
			return "", errors.New("invalid original PostgreSQL catalog seed record")
		}
		if entry.Kind == "catalog_class" {
			continue // The five class descriptors are always read independently.
		}
		if entry.Kind == "database" || entry.Kind == "namespace" {
			oid := original.DatabaseOID
			if entry.Kind == "namespace" {
				oid = original.NamespaceOID
			}
			if entry.OID != oid || entry.SubID != 0 {
				return "", schemaddl.ErrConflict
			}
			continue // The independently checked header selects these records.
		}
		if entry.Kind == "dependency" {
			var edges []map[string]json.RawMessage
			if entry.SubID != 0 || json.Unmarshal(fields["edges"], &edges) != nil || edges == nil {
				return "", errors.New("invalid original PostgreSQL dependency seeds")
			}
			for _, edge := range edges {
				class, ce := postgresOriginalOID(edge["classid"])
				oid, oe := postgresOriginalOID(edge["objid"])
				if ce != nil || oe != nil || oid != entry.OID {
					return "", errors.New("invalid original PostgreSQL dependency seed identity")
				}
				if err := add(postgresOriginalSeed{ClassOID: class, OID: oid}); err != nil {
					return "", err
				}
			}
			continue
		}
		if strings.HasPrefix(entry.Kind, "security_label:") {
			class, err := strconv.ParseUint(strings.TrimPrefix(entry.Kind, "security_label:"), 10, 32)
			if err != nil || class == 0 {
				return "", errors.New("invalid original PostgreSQL label seed class")
			}
			if err := add(postgresOriginalSeed{ClassOID: int64(class), OID: entry.OID}); err != nil {
				return "", err
			}
			continue
		}
		catalog := postgresOriginalSeedCatalog(entry.Kind)
		if catalog == "" {
			return "", errors.New("unsupported original PostgreSQL catalog seed kind")
		}
		oid := entry.OID
		if entry.Kind == "enum" {
			// pg_enum has a distinct native OID; the existing loader reads all
			// members of a required enum type, not a fictitious type at e.oid.
			var err error
			oid, err = postgresOriginalOID(fields["enumtypid"])
			if err != nil {
				return "", err
			}
		}
		if err := add(postgresOriginalSeed{Catalog: catalog, OID: oid}); err != nil {
			return "", err
		}
	}
	ordered := make([]postgresOriginalSeed, 0, len(seeds))
	for seed := range seeds {
		ordered = append(ordered, seed)
	}
	sort.Slice(ordered, func(i, j int) bool {
		a, b := ordered[i], ordered[j]
		if a.Catalog != b.Catalog {
			return a.Catalog < b.Catalog
		}
		if a.ClassOID != b.ClassOID {
			return a.ClassOID < b.ClassOID
		}
		return a.OID < b.OID
	})
	raw, err := json.Marshal(ordered)
	if err != nil {
		return "", err
	}
	if len(raw) > 1<<20 {
		return "", errors.New("original PostgreSQL catalog seed parameters exceed size limit")
	}
	return string(raw), nil
}

func postgresOriginalSeedCatalog(kind string) string {
	return map[string]string{
		"relation": "pg_class", "column": "pg_class", "index": "pg_class", "sequence": "pg_class", "inheritance": "pg_class", "partition": "pg_class",
		"type": "pg_type", "enum": "pg_type", "range": "pg_type",
		"routine": "pg_proc", "aggregate": "pg_proc",
		"default": "pg_attrdef", "constraint": "pg_constraint", "trigger": "pg_trigger", "rewrite": "pg_rewrite", "policy": "pg_policy",
		"default_acl": "pg_default_acl", "event_trigger": "pg_event_trigger", "collation": "pg_collation", "operator": "pg_operator", "cast": "pg_cast", "access_method": "pg_am",
		"operator_class": "pg_opclass", "operator_family": "pg_opfamily", "access_operator": "pg_amop", "access_procedure": "pg_amproc", "extension": "pg_extension",
		"publication": "pg_publication", "publication_relation": "pg_publication_rel", "publication_namespace": "pg_publication_namespace",
	}[kind]
}

func postgresOriginalOID(raw json.RawMessage) (int64, error) {
	text := string(raw)
	if len(raw) > 0 && raw[0] == '"' {
		if json.Unmarshal(raw, &text) != nil {
			return 0, errors.New("invalid original PostgreSQL catalog OID")
		}
	}
	n, err := strconv.ParseUint(text, 10, 32)
	if err != nil || n == 0 {
		return 0, errors.New("invalid original PostgreSQL catalog OID")
	}
	return int64(n), nil
}
