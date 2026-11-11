package contract

// Request tools only author text. They carry neither a session lease nor
// dispatch authority; generated requests still need ordinary preparation.
type KVRequestToolKind string

const (
	KVToolRead        KVRequestToolKind = "read"
	KVToolList        KVRequestToolKind = "list"
	KVToolRaw         KVRequestToolKind = "raw"
	KVToolPut         KVRequestToolKind = "put"
	KVToolDelete      KVRequestToolKind = "delete"
	KVToolTransaction KVRequestToolKind = "transaction"
)

// Optional unsigned numbers use exact decimal text: empty means omitted,
// while "0" means explicitly supplied. Bytes use canonical padded Base64.
// Key is absolute; a connection prefix is only an initial input suggestion.
type KVRequestToolInput struct {
	Key, ValueBase64, Flags, CompareIndex, TransactionJSON string
	KeysOnly, Recursive                                    bool
}

type KVRequestTool struct {
	Kind    KVRequestToolKind
	Default KVRequestToolInput
}

// Implementations must be immutable, synchronous and free of I/O, credentials
// and session state. A captured builder can author drafts after disconnect.
type KVRequestBuilder interface {
	BuildKVRequest(KVRequestToolKind, KVRequestToolInput) (KVRequest, error)
}

type KVRequestBuilderProvider interface {
	KVRequestBuilder() KVRequestBuilder
}
