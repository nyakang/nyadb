package contract

import (
	"context"
	"errors"
	"slices"
)

var ErrKVMemberBecameLeader = errors.New("original member is now leader; remaining batch targets must run first")
var ErrKVMemberOrderUnknown = errors.New("original member leadership cannot be established")

// IDs/URLs/learner are original directory observations, not incarnations.
// Strings are complete native values; serialization uses exact decimal IDs.
type KVMember struct {
	ID                   uint64
	Name                 string
	PeerURLs, ClientURLs []string
	IsLearner            bool
	NativeExtensions     []byte
}

func (m KVMember) Clone() KVMember {
	m.PeerURLs = slices.Clone(m.PeerURLs)
	m.ClientURLs = slices.Clone(m.ClientURLs)
	m.NativeExtensions = slices.Clone(m.NativeExtensions)
	return m
}

type KVMemberDirectory struct {
	Scope      KVScope
	DocumentID uint64
	Result     KVResult
	Members    []KVMember
	Error      string
}

func (d KVMemberDirectory) Clone() KVMemberDirectory {
	d.Scope = d.Scope.Clone()
	d.Result = d.Result.Clone()
	d.Members = slices.Clone(d.Members)
	for i := range d.Members {
		d.Members[i] = d.Members[i].Clone()
	}
	return d
}

// Snapshot is inspection only. The concrete originating provider validates the
// observation handle when binding a target; a copied DTO has no authority.
type KVMemberObservation interface {
	Snapshot() KVMemberDirectory
	Close() error
}

type KVMemberTarget struct {
	MemberID uint64
	Endpoint string
	// Expected version of this member, not an assumption about the root member
	// or the entire cluster. The candidate's original Status must match it.
	Version string
}

type KVMemberScope struct {
	Original   KVScope
	DocumentID uint64
	LeaseID    uint64
	Directory  KVMemberDirectory
	Member     KVMember
	Endpoint   string
	Version    string
	Handshake  KVResult
}

func (s KVMemberScope) Clone() KVMemberScope {
	s.Original = s.Original.Clone()
	s.Directory = s.Directory.Clone()
	s.Member = s.Member.Clone()
	s.Handshake = s.Handshake.Clone()
	return s
}

type KVMemberResult struct {
	Scope          KVMemberScope
	Sequence       uint64
	DirectoryCheck KVMemberDirectory
	Before         KVResult
	BeforeError    string
	Result         KVResult
	Error          string
}

func (r KVMemberResult) Clone() KVMemberResult {
	r.Scope = r.Scope.Clone()
	r.DirectoryCheck = r.DirectoryCheck.Clone()
	r.Before = r.Before.Clone()
	r.Result = r.Result.Clone()
	return r
}

// The real raw/native preparation is one-use and bound to its original target.
// A cloned request/receipt never becomes a preparation for another member.
type KVMemberPreparation interface {
	Snapshot() KVRequest
	Writes() bool
}

type KVMemberLease interface {
	Scope() KVMemberScope
	CheckMemberLease() error
	ObserveStatus(context.Context) (KVMemberResult, error)
	PrepareDefragment() (KVMemberPreparation, error)
	ExecuteMember(context.Context, KVMemberPreparation) (KVMemberResult, error)
	LastMemberResult() KVMemberResult
	Close() error
}

// Optional ordering protection for a step with other selected targets remaining.
// The final native Status on this same transport must identify a follower before
// dispatch. This is an observation gate, not a CAS or leadership lock.
type KVMemberFollowerPreparation interface {
	PrepareFollowerDefragment() (KVMemberPreparation, error)
}

type KVMemberBinding struct {
	Lease KVMemberLease
	Scope KVMemberScope
	Error string
}

// BindMember must return failed handshake observations too, without leaving
// a live foreign transport or treating an alias as member evidence.
type KVMembers interface {
	ObserveMembers(context.Context) (KVMemberObservation, error)
	BindMember(context.Context, KVMemberObservation, KVMemberTarget) (KVMemberBinding, error)
}
