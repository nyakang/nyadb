// Package cassandra implements the uncompressed Cassandra native protocol v4.
// Each client owns one physical connection and never reconnects or retries.
package cassandra

import (
	"errors"
	"fmt"
)

var (
	ErrInvalid        = errors.New("invalid Cassandra request")
	ErrProtocol       = errors.New("invalid or unsupported Cassandra native response")
	ErrBounds         = errors.New("Cassandra request or response exceeds bounds")
	ErrAuthentication = errors.New("Cassandra authentication rejected or unsupported")
	ErrRetired        = errors.New("Cassandra connection retired; recover explicitly without replay")
	ErrBusy           = errors.New("Cassandra connection has an active request")
)

const (
	MaxFrame     = 16 << 20
	MaxStatement = 8 << 20
	MaxColumns   = 256
	MaxRows      = 10000
	maxCells     = 100000
	maxItems     = 10000
	maxDepth     = 32
	maxDecoded   = 32 << 20
	maxPageState = 64 << 10
)

type Interrupted struct {
	WriteOutcomeUnknown bool
	Cause               error
}

func (e *Interrupted) Error() string {
	return "Cassandra request interrupted; verify writes without replay"
}
func (e *Interrupted) Unwrap() error    { return e.Cause }
func (e *Interrupted) Is(e2 error) bool { return e2 == ErrRetired }

// ServerError retains a native code, without copying potentially secret query
// text or server reasons into logs. A complete error does not prove rollback.
type ServerError struct {
	Code                int32
	WriteOutcomeUnknown bool
}

func (e *ServerError) Error() string   { return fmt.Sprintf("Cassandra native error 0x%04x", e.Code) }
func (e *ServerError) Is(t error) bool { return e.WriteOutcomeUnknown && t == ErrRetired }

type Type struct {
	Code                   uint16
	Custom, Keyspace, Name string
	Arguments              []*Type
	Fields                 []Field
}
type Field struct {
	Name string
	Type *Type
}
type Column struct {
	Keyspace, Table, Name string
	Type                  *Type
}

// Value retains exact native bytes and types. Collections retain ordered entries
// including non-string map keys; UDT/tuple nulls remain distinct from empty values.
// Result memory belongs to its caller. Clone before handing values to workers.
type Value struct {
	Type    *Type
	Null    bool
	Missing bool
	Raw     []byte
	Text    string
	Items   []Value
	Entries []Entry
	Fields  []NamedValue
}
type Entry struct{ Key, Value Value }
type NamedValue struct {
	Name  string
	Value Value
}

func (t *Type) Clone() *Type {
	if t == nil {
		return nil
	}
	v := *t
	v.Arguments = make([]*Type, len(t.Arguments))
	for i, x := range t.Arguments {
		v.Arguments[i] = x.Clone()
	}
	v.Fields = make([]Field, len(t.Fields))
	for i, x := range t.Fields {
		v.Fields[i] = Field{x.Name, x.Type.Clone()}
	}
	return &v
}
func (v Value) Clone() Value {
	out := v
	out.Type = v.Type.Clone()
	out.Raw = append([]byte(nil), v.Raw...)
	out.Items = make([]Value, len(v.Items))
	for i, x := range v.Items {
		out.Items[i] = x.Clone()
	}
	out.Entries = make([]Entry, len(v.Entries))
	for i, x := range v.Entries {
		out.Entries[i] = Entry{x.Key.Clone(), x.Value.Clone()}
	}
	out.Fields = make([]NamedValue, len(v.Fields))
	for i, x := range v.Fields {
		out.Fields[i] = NamedValue{x.Name, x.Value.Clone()}
	}
	return out
}

type Parameter struct {
	Bytes       []byte
	Null, Unset bool
}
type Request struct {
	Statement         string
	Parameters        []Parameter
	PageSize          int
	Cursor            *Cursor
	Consistency       uint16
	SerialConsistency uint16
}
type Cursor struct {
	owner   *Client
	request [32]byte
	state   []byte
	mac     [32]byte
}

func (c *Cursor) Clone() *Cursor {
	if c == nil {
		return nil
	}
	v := *c
	v.state = append([]byte(nil), c.state...)
	return &v
}

type SchemaChange struct {
	Change, Target, Keyspace, Name string
	ArgumentTypes                  []string
}
type Result struct {
	Kind         int32
	Columns      []Column
	Rows         [][]Value
	Next         *Cursor
	Keyspace     string
	SchemaChange *SchemaChange
	Warnings     []string
}
type Identity struct{ HostID, ClusterName, ReleaseVersion, CQLVersion, DataCenter, Rack, Partitioner, SchemaVersion string }
