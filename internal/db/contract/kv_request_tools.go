package contract

// Request tools only author text. They carry neither a session lease nor
// dispatch authority; generated requests still need ordinary preparation.
type KVRequestToolKind string

const (
	KVToolRead                        KVRequestToolKind = "read"
	KVToolList                        KVRequestToolKind = "list"
	KVToolRaw                         KVRequestToolKind = "raw"
	KVToolPut                         KVRequestToolKind = "put"
	KVToolDelete                      KVRequestToolKind = "delete"
	KVToolTransaction                 KVRequestToolKind = "transaction"
	KVToolHTTPSessionInfo             KVRequestToolKind = "http-session-info"
	KVToolHTTPSessionCreate           KVRequestToolKind = "http-session-create"
	KVToolHTTPSessionDestroy          KVRequestToolKind = "http-session-destroy"
	KVToolHTTPAcquire                 KVRequestToolKind = "http-acquire"
	KVToolHTTPRelease                 KVRequestToolKind = "http-release"
	KVToolHTTPSessionList             KVRequestToolKind = "http-session-list"
	KVToolHTTPNodeSessions            KVRequestToolKind = "http-node-sessions"
	KVToolHTTPHealthState             KVRequestToolKind = "http-health-state"
	KVToolHTTPHealthNode              KVRequestToolKind = "http-health-node"
	KVToolHTTPHealthChecks            KVRequestToolKind = "http-health-checks"
	KVToolHTTPHealthService           KVRequestToolKind = "http-health-service"
	KVToolHTTPAgentSelf               KVRequestToolKind = "http-agent-self"
	KVToolHTTPAgentServices           KVRequestToolKind = "http-agent-services"
	KVToolHTTPAgentChecks             KVRequestToolKind = "http-agent-checks"
	KVToolHTTPAgentServiceRegister    KVRequestToolKind = "http-agent-service-register"
	KVToolHTTPAgentServiceDeregister  KVRequestToolKind = "http-agent-service-deregister"
	KVToolHTTPAgentCheckRegister      KVRequestToolKind = "http-agent-check-register"
	KVToolHTTPAgentCheckDeregister    KVRequestToolKind = "http-agent-check-deregister"
	KVToolHTTPAgentCheckPass          KVRequestToolKind = "http-agent-check-pass"
	KVToolHTTPAgentCheckWarn          KVRequestToolKind = "http-agent-check-warn"
	KVToolHTTPAgentCheckFail          KVRequestToolKind = "http-agent-check-fail"
	KVToolHTTPAgentCheckUpdate        KVRequestToolKind = "http-agent-check-update"
	KVToolHTTPAgentNodeMaintenance    KVRequestToolKind = "http-agent-node-maintenance"
	KVToolHTTPAgentServiceMaintenance KVRequestToolKind = "http-agent-service-maintenance"
)

// HTTP Session UUIDs are distinct from numeric leases. Empty duration, behavior
// and NodeChecksJSON fields mean omitted; "0s" and "[]" are explicit values.
// Name and Node can be explicitly empty. All fields are values so drafts and
// presentation snapshots remain independently copyable and comparable.
type KVHTTPSessionToolInput struct {
	ID, Name, Node, TTL, LockDelay, Behavior, NodeChecksJSON string
	IncludeName, IncludeNode                                 bool
}

// Health tools only author original reads. Empty Passing/Index/Wait omit those
// native options; "false", "0" and explicit Filter/Near inclusion stay distinct.
// Values remain comparable drafts without pointers, credentials or authority.
type KVHTTPHealthToolInput struct {
	State, Node, Service, Passing, Filter, Near, Index, Wait string
	IncludeFilter, IncludeNear                               bool
}

// Agent reads contact the original local agent. IncludeFilter preserves the
// distinction between an omitted filter and an explicitly empty native filter.
type KVHTTPAgentToolInput struct {
	Filter        string
	IncludeFilter bool
}

// Service tools retain the entire original definition and native ID. Explicit
// replacement includes empty, false and true; omission remains distinct.
// Comparable values keep draft and file snapshots independent of execution.
type KVHTTPAgentServiceToolInput struct {
	ID, DefinitionJSON, ReplaceExistingChecks string
	IncludeReplacement                        bool
}

// Check tools retain the complete native definition and original check ID.
// Comparable strings keep authored drafts independent of execution authority.
type KVHTTPAgentCheckToolInput struct {
	ID, DefinitionJSON string
}

// TTL status tools preserve note presence. NoteJSON is an authored JSON string
// decoded by the pure builder, so newline/control values remain exact. An empty
// field with IncludeNote means an explicitly empty note. Update retains complete
// Status/Output JSON. Values stay comparable without execution authority.
type KVHTTPAgentTTLToolInput struct {
	ID, NoteJSON, DefinitionJSON string
	IncludeNote                  bool
}

// Maintenance input retains the original native boolean spelling and reason
// presence. ReasonJSON is an authored JSON string that preserves control text.
// Node requests reject a service ID; these comparable values carry no authority.
type KVHTTPAgentMaintenanceToolInput struct {
	ID, Enable, ReasonJSON string
	IncludeReason          bool
}

// Optional unsigned numbers use exact decimal text: empty means omitted,
// while "0" means explicitly supplied. Bytes use canonical padded Base64.
// Key is absolute; a connection prefix is only an initial input suggestion.
type KVRequestToolInput struct {
	Key, ValueBase64, Flags, CompareIndex, TransactionJSON string
	KeysOnly, Recursive                                    bool
	HTTPSession                                            KVHTTPSessionToolInput
	HTTPHealth                                             KVHTTPHealthToolInput
	HTTPAgent                                              KVHTTPAgentToolInput
	HTTPAgentService                                       KVHTTPAgentServiceToolInput
	HTTPAgentCheck                                         KVHTTPAgentCheckToolInput
	HTTPAgentTTL                                           KVHTTPAgentTTLToolInput
	HTTPAgentMaintenance                                   KVHTTPAgentMaintenanceToolInput
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
