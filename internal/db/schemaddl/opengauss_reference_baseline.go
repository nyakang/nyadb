package schemaddl

import "errors"

// OpenGaussReferenceBaseline retains a complete independent native reference
// catalog before any private FK installs temporary parent-side RI triggers.
// It grants no execution permission and cannot make the reference editable.
type OpenGaussReferenceBaseline struct {
	original, desired, revision string
	catalog                     *OpenGaussCatalog
}

func CaptureOpenGaussReferenceBaseline(original Snapshot, desired Table, catalog *OpenGaussCatalog) (OpenGaussReferenceBaseline, error) {
	if original.Engine != "opengauss" || original.OpenGauss == nil || catalog == nil || catalog.RelationOID == 0 {
		return OpenGaussReferenceBaseline{}, ErrConflict
	}
	if _, err := PlanOpenGauss(original, desired, original.OpenGauss.Namespace, true); err != nil {
		return OpenGaussReferenceBaseline{}, err
	}
	revision, err := catalog.Fingerprint()
	if err != nil {
		return OpenGaussReferenceBaseline{}, err
	}
	prior := original.OpenGauss
	if catalog.Database != prior.Database || catalog.DatabaseOID != prior.DatabaseOID || catalog.RoleOID != prior.RoleOID || catalog.NativeByteOrder != prior.NativeByteOrder || Signature(catalog.Classes) != Signature(prior.Classes) || catalog.Hooks || prior.Hooks {
		return OpenGaussReferenceBaseline{}, ErrConflict
	}
	if catalog.Namespace == prior.Namespace && catalog.NamespaceOID != prior.NamespaceOID || catalog.NamespaceOID == prior.NamespaceOID && catalog.Namespace != prior.Namespace || catalog.RelationOID == prior.RelationOID && (catalog.Name != prior.Name || catalog.Namespace != prior.Namespace) {
		return OpenGaussReferenceBaseline{}, ErrConflict
	}
	matched := false
	for _, table := range []Table{original.Table, desired} {
		refs, err := OpenGaussDesignReferencedTables(table, prior.Namespace)
		if err != nil {
			return OpenGaussReferenceBaseline{}, err
		}
		for _, ref := range refs {
			matched = matched || ref.Schema == catalog.Namespace && ref.Name == catalog.Name
		}
	}
	if !matched {
		return OpenGaussReferenceBaseline{}, errors.New("native reference has no original or requested foreign key")
	}
	for _, entry := range catalog.Entries {
		if entry.Kind == "copy_definition" || entry.Kind == "design_definition" {
			return OpenGaussReferenceBaseline{}, errors.New("native reference baseline must omit synthesized declarations and sequence state")
		}
		if entry.Kind == "relation" && entry.OID == catalog.RelationOID {
			row, err := gaussJSON(entry.Data)
			if err != nil || row["relname"] != catalog.Name || row["relkind"] != "r" || row["relpersistence"] != "p" && row["relpersistence"] != "u" {
				return OpenGaussReferenceBaseline{}, errors.Join(err, ErrConflict)
			}
		}
	}
	return OpenGaussReferenceBaseline{original: Signature(original), desired: Signature(desired), revision: revision, catalog: catalog.Clone()}, nil
}

// Before a reference transition, every native identity, permission, dependency
// and unknown property must still match the captured request and independent
// catalog. Temporary FK changes require their own explicit transition proof;
// they are not normalized away by this unchanged-context check.
func VerifyOpenGaussReferenceUnchanged(baseline OpenGaussReferenceBaseline, original Snapshot, desired Table, actual *OpenGaussCatalog) error {
	if baseline.catalog == nil || actual == nil || baseline.original != Signature(original) || baseline.desired != Signature(desired) {
		return ErrConflict
	}
	before, err := baseline.catalog.Fingerprint()
	if err != nil || before != baseline.revision {
		return errors.Join(err, ErrConflict)
	}
	after, err := actual.Fingerprint()
	if err != nil || after != before {
		return errors.Join(err, ErrConflict)
	}
	return nil
}
