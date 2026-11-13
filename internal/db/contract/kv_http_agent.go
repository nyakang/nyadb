package contract

import (
	"maps"
	"slices"
)

// Agent entries belong to one captured local-agent map. Key retains the native
// map identity; it is not a catalog identity or evidence of write permission.
// Raw retains the complete original JSON value, including unknown fields.
type KVHTTPAgentEntry struct {
	Key, ID, Name, Node, ServiceID, Service, Status, Notes, Output string
	Kind, Type, Address, Namespace, Partition                      string
	PresentFields, NullFields, Tags                                []string
	Meta                                                           map[string]string
	Port, ExposedPort                                              uint64
	Raw                                                            []byte
}

func (e KVHTTPAgentEntry) Clone() KVHTTPAgentEntry {
	e.PresentFields, e.NullFields = slices.Clone(e.PresentFields), slices.Clone(e.NullFields)
	e.Tags, e.Meta, e.Raw = slices.Clone(e.Tags), maps.Clone(e.Meta), slices.Clone(e.Raw)
	return e
}

// Complete describes this returned map's projection and ACL boundary. It does
// not prove an Agent incarnation, catalog inventory or authority to mutate it.
type KVHTTPAgentList struct {
	Kind                         string
	Rows                         []KVHTTPAgentEntry
	ProjectionComplete, Complete bool
}

func (l KVHTTPAgentList) Clone() KVHTTPAgentList {
	l.Rows = slices.Clone(l.Rows)
	for i := range l.Rows {
		l.Rows[i] = l.Rows[i].Clone()
	}
	return l
}
