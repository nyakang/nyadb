package sqlworkspace

func (s *State) profileExists(id int64) bool {
	if s.deps.Profiles == nil {
		return id > 0
	}
	for _, p := range s.deps.Profiles() {
		if p.ID == id {
			return true
		}
	}
	return false
}

// Rebind changes presentation scope only. A new lease requires explicit recovery.
func (s *State) Rebind(d *Document, profile int64) {
	if d == nil || d.closed || d.queryBusy() || !d.recoveryNeeded || !s.profileExists(profile) {
		return
	}
	s.dispose(d)
	d.profileID, d.catalog, d.namespaceSchema = 0, "", ""
	d.recoveryProfile = profile
	s.applyQueryDialect(d, profile)
	d.recoveryCatalog, d.recoveryNamespaceSchema = "", d.recoverySchema
	if s.deps.Namespace != nil {
		n := s.deps.Namespace(profile, d.recoverySchema)
		d.recoveryCatalog, d.recoveryNamespaceSchema = n.Catalog, n.Schema
	}
	d.result, d.plan, d.ordinaryReading, d.explainReading = nil, nil, nil, nil
	d.err = ""
}
