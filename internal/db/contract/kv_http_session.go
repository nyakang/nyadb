package contract

import "slices"

// Session fields are observations of one original HTTP response. Presence and
// null remain distinct from an empty string, empty check list, or numeric zero.
// Unknown fields retain their names here and their values in the HTTP body.
// Indices are native unsigned values, not etcd lease IDs or MVCC revisions.
type KVHTTPSession struct {
	PresentFields, NullFields                           []string
	ID, Name, Node, TTL, Behavior, Namespace, Partition string
	LockDelay, CreateIndex, ModifyIndex                 uint64
	Checks, NodeChecks                                  []string
	ServiceChecks                                       []KVHTTPSessionServiceCheck
}

type KVHTTPSessionServiceCheck struct {
	PresentFields, NullFields []string
	ID, Namespace             string
}

func (c KVHTTPSessionServiceCheck) Clone() KVHTTPSessionServiceCheck {
	c.PresentFields = slices.Clone(c.PresentFields)
	c.NullFields = slices.Clone(c.NullFields)
	return c
}
func (s KVHTTPSession) Clone() KVHTTPSession {
	s.PresentFields = slices.Clone(s.PresentFields)
	s.NullFields = slices.Clone(s.NullFields)
	s.Checks = slices.Clone(s.Checks)
	s.NodeChecks = slices.Clone(s.NodeChecks)
	s.ServiceChecks = slices.Clone(s.ServiceChecks)
	for i := range s.ServiceChecks {
		s.ServiceChecks[i] = s.ServiceChecks[i].Clone()
	}
	return s
}

// A non-nil list is a complete projection of the captured response, including
// an empty array. It is not proof of all cluster Sessions: HTTP ACL filtering,
// original scope and consistency still apply. Rows retain server response order.
// A malformed or excessive projection is unavailable; it never replaces the
// native body, acknowledgement or original unknown-write receipt.
type KVHTTPSessionList struct{ Rows []KVHTTPSession }

func (l KVHTTPSessionList) Clone() KVHTTPSessionList {
	l.Rows = slices.Clone(l.Rows)
	for i := range l.Rows {
		l.Rows[i] = l.Rows[i].Clone()
	}
	return l
}
