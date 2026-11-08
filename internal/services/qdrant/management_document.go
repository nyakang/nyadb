package qdrant

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"sync"

	"github.com/nyakang/nyadb/internal/db/contract"
)

var ErrManagementInUse = errors.New("close the original REST and affected point documents before managing the collection or aliases")

// ManagementObservation binds bounded original native responses to one lease.
// It captures live configurations and aliases, never remote incarnation or CAS.
type ManagementObservation struct {
	ProfileID   int64
	Sequence    uint64
	Targets     []string
	Collections []string
	Aliases     []Alias
	Configs     map[string]json.RawMessage
	Responses   []ManagementReadResponse
	Token       string
}

type ManagementReadResponse struct {
	Response          Response
	BodyTruncated     bool
	OriginalBodyBytes int
}

func cloneManagementObservation(o ManagementObservation) ManagementObservation {
	o.Targets, o.Collections, o.Aliases = slices.Clone(o.Targets), slices.Clone(o.Collections), slices.Clone(o.Aliases)
	o.Configs = cloneRawMap(o.Configs)
	o.Responses = slices.Clone(o.Responses)
	for i := range o.Responses {
		o.Responses[i].Response.Body = bytes.Clone(o.Responses[i].Response.Body)
	}
	return o
}
func cloneRawMap(m map[string]json.RawMessage) map[string]json.RawMessage {
	copy := make(map[string]json.RawMessage, len(m))
	for key, value := range m {
		copy[key] = bytes.Clone(value)
	}
	return copy
}

type ManagedPlan struct {
	lease    *ManagementLease
	sequence uint64
	before   ManagementObservation
	native   ManagementPlan
	aliases  []AliasChange
	claims   []string
}

func (p ManagedPlan) Preview() Request { return p.native.Preview() }
func (p ManagedPlan) Action() string   { return p.native.Action() }

type managementPending struct {
	plan    ManagedPlan
	receipt ManagementReceipt
	last    ManagedResult
}

// ManagementLease owns immutable preflight/attempt/evidence state on the
// captured original session. UI/native-public generations belong to dbruntime.
type ManagementLease struct {
	mu, operation sync.Mutex
	session       *Session
	document      uint64
	ctx           context.Context
	cancel        context.CancelFunc
	closed        bool
	done          chan struct{}
	key           [32]byte
	sequence      uint64
	planSequence  uint64
	observation   *ManagementObservation
	pending       *managementPending
}

func (s *Session) ReserveManagementDocument(document uint64) (*ManagementLease, error) {
	if document == 0 {
		return nil, ErrInvalid
	}
	l := &ManagementLease{session: s, document: document, done: make(chan struct{})}
	if _, err := rand.Read(l.key[:]); err != nil {
		return nil, err
	}
	l.ctx, l.cancel = context.WithCancel(s.ctx)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.revoked {
		l.cancel()
		return nil, contract.ErrRevoked
	}
	if len(s.managers) >= 8 {
		l.cancel()
		return nil, ErrBounds
	}
	for m := range s.managers {
		if m.document == document {
			l.cancel()
			return nil, ErrInvalid
		}
	}
	for snapshot := range s.snapshots {
		if snapshot.document == document {
			l.cancel()
			return nil, ErrInvalid
		}
	}
	if s.managers == nil {
		s.managers = map[*ManagementLease]bool{}
	}
	s.managers[l] = true
	return l, nil
}

func (l *ManagementLease) begin(ctx context.Context) (context.Context, func(), error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil, nil, contract.ErrRevoked
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	child, done, err := l.session.request(ctx)
	if err != nil {
		return nil, nil, err
	}
	child, cancel := context.WithCancel(child)
	stop := context.AfterFunc(l.ctx, cancel)
	return child, func() { stop(); cancel(); done() }, nil
}
func (l *ManagementLease) sign(o ManagementObservation) string {
	o.Token = ""
	raw, _ := json.Marshal(o)
	h := hmac.New(sha256.New, l.key[:])
	h.Write(raw)
	return hex.EncodeToString(h.Sum(nil))
}
func (l *ManagementLease) read(ctx context.Context, c *Client, targets []string) (ManagementObservation, error) {
	o := ManagementObservation{ProfileID: l.session.ProfileID(), Targets: slices.Clone(targets), Configs: map[string]json.RawMessage{}}
	var err error
	o.Targets, err = captureManagementTargets(targets)
	if err != nil {
		return o, err
	}
	bytesRead := 0
	keep := func(r Response) error {
		e := ManagementReadResponse{Response: r, OriginalBodyBytes: len(r.Body)}
		if len(r.Body) > requestLimit-bytesRead {
			e.BodyTruncated = true
			e.Response.Body = bytes.Clone(r.Body[:requestLimit-bytesRead])
		}
		bytesRead += len(e.Response.Body)
		o.Responses = append(o.Responses, e)
		if e.BodyTruncated {
			return ErrBounds
		}
		return nil
	}
	names, response, err := c.Collections(ctx)
	if e := keep(response); e != nil {
		return o, e
	}
	if err != nil {
		return o, err
	}
	if len(names) > metadataLimit {
		return o, ErrBounds
	}
	o.Collections = names
	aliases, response, err := c.Aliases(ctx)
	if e := keep(response); e != nil {
		return o, e
	}
	if err != nil {
		return o, err
	}
	if len(aliases) > metadataLimit {
		return o, ErrBounds
	}
	o.Aliases = aliases
	for _, name := range o.Targets {
		if !slices.Contains(names, name) {
			continue
		}
		info, response, err := c.Collection(ctx, name)
		if e := keep(response); e != nil {
			return o, e
		}
		if err != nil {
			return o, err
		}
		fields, err := object(info.Result)
		if err != nil {
			return o, err
		}
		config, err := canonicalJSON(fields["config"])
		if err != nil {
			return o, err
		}
		o.Configs[name] = config
	}
	if err := l.session.validateObservation(ctx, c); err != nil {
		return o, err
	}
	return o, nil
}

func captureManagementTargets(targets []string) ([]string, error) {
	if len(targets) == 0 || len(targets) > 100 {
		return nil, ErrBounds
	}
	copy := slices.Clone(targets)
	slices.Sort(copy)
	if len(slices.Compact(slices.Clone(copy))) != len(copy) {
		return nil, ErrInvalid
	}
	for _, name := range copy {
		if _, err := collectionPath(name); err != nil {
			return nil, err
		}
	}
	return copy, nil
}

func (l *ManagementLease) Read(ctx context.Context, targets []string) (ManagementObservation, error) {
	if !l.operation.TryLock() {
		return ManagementObservation{}, ErrBusy
	}
	defer l.operation.Unlock()
	targets, err := captureManagementTargets(targets)
	if err != nil {
		return ManagementObservation{}, err
	}
	if l.pending != nil {
		return ManagementObservation{}, contract.ErrNamespaceBusy
	}
	ctx, done, err := l.begin(ctx)
	if err != nil {
		return ManagementObservation{}, err
	}
	defer done()
	c, err := l.session.open(ctx)
	if err != nil {
		return ManagementObservation{}, err
	}
	defer c.Close()
	l.sequence++
	l.planSequence++
	l.observation = nil
	o, err := l.read(ctx, c, targets)
	if err != nil {
		return o, err
	}
	o.Sequence = l.sequence
	o.Token = l.sign(o)
	snapshot := cloneManagementObservation(o)
	l.observation = &snapshot
	return o, nil
}

func (l *ManagementLease) validateBefore(o ManagementObservation) error {
	if l.observation == nil || l.pending != nil || o.ProfileID != l.session.ProfileID() || o.Sequence != l.sequence || len(o.Token) != 64 || o.Token != l.observation.Token {
		return contract.ErrNamespaceChanged
	}
	if len(o.Targets) > 100 || len(o.Collections) > metadataLimit || len(o.Aliases) > metadataLimit || len(o.Configs) > 100 || len(o.Responses) > 102 {
		return ErrBounds
	}
	size := 0
	for _, group := range [][]string{o.Targets, o.Collections} {
		for _, name := range group {
			if len(name) > 255 {
				return ErrBounds
			}
			size += len(name)
		}
	}
	for _, a := range o.Aliases {
		if len(a.Alias) > 255 || len(a.Collection) > 255 {
			return ErrBounds
		}
		size += len(a.Alias) + len(a.Collection)
	}
	for name, raw := range o.Configs {
		if len(name) > 255 || len(raw) > requestLimit {
			return ErrBounds
		}
		size += len(name) + len(raw)
	}
	for _, r := range o.Responses {
		if len(r.Response.Body) > requestLimit || len(r.Response.ContentType) > 64<<10 {
			return ErrBounds
		}
		size += len(r.Response.Body) + len(r.Response.ContentType)
	}
	if size > 3*requestLimit {
		return ErrBounds
	}
	if !hmac.Equal([]byte(o.Token), []byte(l.sign(o))) {
		return contract.ErrNamespaceChanged
	}
	return nil
}

func (l *ManagementLease) Close() error {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		<-l.done
		return nil
	}
	l.closed = true
	l.cancel()
	l.mu.Unlock()
	l.operation.Lock()
	l.pending = nil
	l.observation = nil
	clear(l.key[:])
	l.session.mu.Lock()
	delete(l.session.managers, l)
	if l.session.namespaceOwner == l {
		l.session.namespaceOwner = nil
		l.session.claimedCollections = nil
	}
	l.session.mu.Unlock()
	l.operation.Unlock()
	close(l.done)
	return nil
}
