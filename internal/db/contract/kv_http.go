package contract

import "slices"

const KVConsulHTTPRequestMethod = "consul.http"

// HTTP identity records the original configured endpoint and actual optional
// agent observation. Neither endpoint equality nor agent ID proves incarnation.
// Configuration contains a fingerprint of non-secret configuration only.
type KVHTTPIdentity struct {
	Protocol, Endpoint, Datacenter, Consistency, Auth, Configuration string
	AgentID, AgentName, AgentDatacenter                              string
	AgentAvailable                                                   bool
}

type KVHTTPRequest struct {
	Method, Path string
	Query        map[string][]string
	Body         []byte
}

func cloneHTTPFields(fields map[string][]string) map[string][]string {
	if fields == nil {
		return nil
	}
	out := make(map[string][]string, len(fields))
	for key, values := range fields {
		out[key] = slices.Clone(values)
	}
	return out
}

func (r KVHTTPRequest) Clone() KVHTTPRequest {
	r.Query = cloneHTTPFields(r.Query)
	r.Body = slices.Clone(r.Body)
	return r
}

type KVHTTPMetadata struct {
	Index, LastContact             *uint64
	KnownLeader, ACLFiltered       *bool
	QueryBackend, DefaultACLPolicy string
}

func clonePointer[T any](p *T) *T {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}

func (m KVHTTPMetadata) Clone() KVHTTPMetadata {
	m.Index = clonePointer(m.Index)
	m.LastContact = clonePointer(m.LastContact)
	m.KnownLeader = clonePointer(m.KnownLeader)
	m.ACLFiltered = clonePointer(m.ACLFiltered)
	return m
}

// Presence stays explicit; indices/flags are never coerced to signed MVCC
// revisions. The complete original response retains all unknown field values.
type KVHTTPEntry struct {
	PresentFields                              []string
	Key                                        string
	Value                                      []byte
	ValuePresent, ValueNull                    bool
	Flags, CreateIndex, ModifyIndex, LockIndex uint64
	Session                                    string
	SessionNull                                bool
}

func (e KVHTTPEntry) Clone() KVHTTPEntry {
	e.PresentFields = slices.Clone(e.PresentFields)
	e.Value = slices.Clone(e.Value)
	return e
}

type KVHTTPTransactionError struct {
	OpIndex int
	What    string
}
type KVHTTPTransaction struct {
	Committed bool
	Results   []KVHTTPEntry
	Errors    []KVHTTPTransactionError
}

func (t KVHTTPTransaction) Clone() KVHTTPTransaction {
	t.Results = slices.Clone(t.Results)
	for i := range t.Results {
		t.Results[i] = t.Results[i].Clone()
	}
	t.Errors = slices.Clone(t.Errors)
	return t
}

// Body contains original bytes, including an observed incomplete prefix.
// Captured/EOF, executor Ended, native ACK and success are distinct. Header and
// Trailer retain parsed HTTP values, not an assertion of raw wire header bytes.
// This payload never occupies KVResult.Native's protobuf field.
type KVHTTPResult struct {
	Agent                                                                                   *KVHTTPAgentList
	Health                                                                                  *KVHTTPHealthList
	URL                                                                                     string
	Request                                                                                 KVHTTPRequest
	Kind, Protocol                                                                          string
	Attempted, Ended, Captured, EOF, Acknowledged, Succeeded, WriteOutcomeUnknown, Canceled bool
	Status                                                                                  int
	Header, Trailer                                                                         map[string][]string
	Body                                                                                    []byte
	Metadata                                                                                KVHTTPMetadata
	Entries                                                                                 []KVHTTPEntry
	Txn                                                                                     *KVHTTPTransaction
	Sessions                                                                                *KVHTTPSessionList
	SessionID                                                                               string
	CaptureError, ProjectionError                                                           string
}

func (r KVHTTPResult) Clone() KVHTTPResult {
	r.Request = r.Request.Clone()
	r.Header = cloneHTTPFields(r.Header)
	r.Trailer = cloneHTTPFields(r.Trailer)
	r.Body = slices.Clone(r.Body)
	r.Metadata = r.Metadata.Clone()
	r.Entries = slices.Clone(r.Entries)
	for i := range r.Entries {
		r.Entries[i] = r.Entries[i].Clone()
	}
	if r.Health != nil {
		v := r.Health.Clone()
		r.Health = &v
	}
	if r.Agent != nil {
		v := r.Agent.Clone()
		r.Agent = &v
	}
	if r.Sessions != nil {
		v := r.Sessions.Clone()
		r.Sessions = &v
	}
	if r.Txn != nil {
		v := r.Txn.Clone()
		r.Txn = &v
	}
	return r
}
