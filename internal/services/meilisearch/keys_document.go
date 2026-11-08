package meilisearch

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nyakang/nyadb/internal/db/contract"
)

const keyDraftLimit = 1 << 20
const keyMask = "[hidden]"

type keyEntry struct {
	record contract.KeyRecord
	secret []byte
}
type keysDocument struct {
	bound          *queryLease
	proof          *taskDocument
	object         contract.ObjectRef
	entries        map[string]keyEntry
	page           contract.KeyPage
	last           contract.KeyResult
	sequence, used uint64
	uncertain      bool
}

func cloneKey(r contract.KeyRecord) contract.KeyRecord {
	r.Actions = slices.Clone(r.Actions)
	r.Indexes = slices.Clone(r.Indexes)
	return r
}
func cloneKeyPage(p contract.KeyPage) contract.KeyPage {
	p.Results = slices.Clone(p.Results)
	for i := range p.Results {
		p.Results[i] = cloneKey(p.Results[i])
	}
	return p
}
func cloneKeyResult(r contract.KeyResult) contract.KeyResult { r.Record = cloneKey(r.Record); return r }
func cloneKeyDraft(d contract.KeyDraft) contract.KeyDraft {
	d.Actions = slices.Clone(d.Actions)
	d.Indexes = slices.Clone(d.Indexes)
	return d
}
func validKeyUID(uid string) bool {
	if len(uid) != 36 {
		return false
	}
	for i, ch := range uid {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if ch != '-' {
				return false
			}
		} else if !(ch >= '0' && ch <= '9' || ch >= 'a' && ch <= 'f') {
			return false
		}
	}
	return true
}
func (s *Session) SupportsKeysDocument(o contract.ObjectRef) bool {
	return s.Alive() && s.config.IndexUID == "" && o.ProfileID == s.profileID && o.Catalog == "" && o.Schema == "" && o.Name == "keys" && o.Kind == "keys"
}
func (s *Session) ReserveKeysDocument(o contract.ObjectRef) (contract.KeysDocumentLease, error) {
	if !s.Alive() {
		return nil, contract.ErrRevoked
	}
	if !s.SupportsKeysDocument(o) {
		return nil, contract.ErrUnsupported
	}
	l, err := s.ReserveQuery(1)
	if err != nil {
		return nil, err
	}
	bound := l.(*queryLease)
	proof := &taskDocument{bound: bound, object: o}
	if _, err = rand.Read(proof.key[:]); err != nil {
		l.Release()
		return nil, ErrInvalid
	}
	return &keysDocument{bound: bound, proof: proof, object: o, entries: map[string]keyEntry{}}, nil
}
func (d *keysDocument) alive() error {
	d.bound.mu.Lock()
	defer d.bound.mu.Unlock()
	if d.bound.released {
		return contract.ErrRevoked
	}
	if d.bound.lost {
		return contract.ErrKeyLeaseLost
	}
	if !d.bound.s.Alive() {
		return contract.ErrRevoked
	}
	return nil
}
func (d *keysDocument) keyError(err error, raw string) error {
	if errors.Is(err, ErrRetired) || errors.Is(err, contract.ErrTaskLeaseLost) || errors.Is(err, contract.ErrSQLSessionLost) || errors.Is(err, contract.ErrRevoked) {
		d.bound.mu.Lock()
		d.bound.lost = true
		d.bound.mu.Unlock()
		err = errors.Join(contract.ErrKeyLeaseLost, err)
	}
	return &contract.KeyRequestError{Raw: raw, Cause: err}
}
func (d *keysDocument) request(ctx context.Context) (context.Context, func(), error) {
	c, done, err := d.proof.request(ctx)
	if err != nil {
		return nil, nil, d.keyError(err, "")
	}
	return c, done, nil
}
func (d *keysDocument) publicJSON(raw []byte) string {
	var value any
	if decodeJSON(raw, &value) != nil {
		return "[unavailable native response]"
	}
	d.bound.s.mu.Lock()
	credential := d.bound.s.secret
	d.bound.s.mu.Unlock()
	secrets := [][]byte{[]byte(credential)}
	for _, e := range d.entries {
		secrets = append(secrets, e.secret)
	}
	var collect func(any)
	collect = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			for k, item := range x {
				switch strings.ToLower(k) {
				case "key", "apikey", "masterkey", "authorization", "token", "password", "secret":
					if text, ok := item.(string); ok && text != "" {
						secrets = append(secrets, []byte(text))
					}
				}
				collect(item)
			}
		case []any:
			for _, item := range x {
				collect(item)
			}
		}
	}
	collect(value)
	var redact func(any) any
	redact = func(v any) any {
		switch x := v.(type) {
		case map[string]any:
			for k, item := range x {
				switch strings.ToLower(k) {
				case "key", "apikey", "masterkey", "authorization", "token", "password", "secret":
					x[k] = keyMask
				default:
					x[k] = redact(item)
				}
			}
		case []any:
			for i := range x {
				x[i] = redact(x[i])
			}
		case string:
			for _, secret := range secrets {
				if len(secret) > 0 {
					x = strings.ReplaceAll(x, string(secret), keyMask)
				}
			}
			return x
		}
		return v
	}
	out, err := json.Marshal(redact(value))
	if err != nil {
		return "[unavailable native response]"
	}
	return string(out)
}
func (d *keysDocument) recordProof(r contract.KeyRecord) string {
	r.Token = ""
	raw, _ := json.Marshal(r)
	sum := sha256.Sum256(raw)
	return d.proof.sign("key-record", sum[:])
}
func (d *keysDocument) planProof(p contract.KeyPlan) string {
	p.Token = ""
	raw, _ := json.Marshal(p)
	sum := sha256.Sum256(raw)
	return d.proof.sign("key-plan", sum[:])
}
func nullableKeyString(raw json.RawMessage) (string, error) {
	if bytes.Equal(raw, []byte("null")) {
		return "", nil
	}
	return nativeString(raw)
}
func keyStrings(raw json.RawMessage) ([]string, error) {
	var values []string
	if decodeJSON(raw, &values) != nil || values == nil || len(values) > 256 {
		return nil, ErrProtocol
	}
	seen := map[string]bool{}
	for _, s := range values {
		if s == "" || len(s) > 512 || seen[s] {
			return nil, ErrProtocol
		}
		seen[s] = true
	}
	return values, nil
}
func (d *keysDocument) parse(raw []byte) (keyEntry, error) {
	if len(raw) > keyDraftLimit {
		return keyEntry{}, ErrBounds
	}
	fields, err := nativeObject(raw)
	if err != nil {
		return keyEntry{}, err
	}
	r := contract.KeyRecord{}
	r.UID, err = nativeString(fields["uid"])
	if err != nil || !validKeyUID(r.UID) {
		return keyEntry{}, ErrProtocol
	}
	r.Name, err = nullableKeyString(fields["name"])
	if err != nil {
		return keyEntry{}, err
	}
	r.Description, err = nullableKeyString(fields["description"])
	if err != nil {
		return keyEntry{}, err
	}
	r.ExpiresAt, err = nullableKeyString(fields["expiresAt"])
	if err != nil {
		return keyEntry{}, err
	}
	if r.ExpiresAt != "" {
		if _, err = time.Parse(time.RFC3339Nano, r.ExpiresAt); err != nil {
			return keyEntry{}, ErrProtocol
		}
	}
	r.CreatedAt, err = nativeString(fields["createdAt"])
	if err != nil {
		return keyEntry{}, err
	}
	r.UpdatedAt, err = nativeString(fields["updatedAt"])
	if err != nil {
		return keyEntry{}, err
	}
	for _, s := range []string{r.CreatedAt, r.UpdatedAt} {
		if len(s) > 64 {
			return keyEntry{}, ErrProtocol
		}
		if _, err = time.Parse(time.RFC3339Nano, s); err != nil {
			return keyEntry{}, ErrProtocol
		}
	}
	r.Actions, err = keyStrings(fields["actions"])
	if err != nil {
		return keyEntry{}, err
	}
	r.Indexes, err = keyStrings(fields["indexes"])
	if err != nil {
		return keyEntry{}, err
	}
	secret, err := nativeString(fields["key"])
	if err != nil || len(secret) != 64 {
		return keyEntry{}, ErrProtocol
	}
	if _, err = hex.DecodeString(secret); err != nil {
		return keyEntry{}, ErrProtocol
	}
	canonical, err := json.Marshal(fields)
	if err != nil {
		return keyEntry{}, ErrProtocol
	}
	sum := sha256.Sum256(canonical)
	clear(canonical)
	r.Fingerprint = hex.EncodeToString(sum[:])
	r.Raw = d.publicJSON(raw)
	safeFields, e := nativeObject([]byte(r.Raw))
	if e != nil {
		return keyEntry{}, ErrProtocol
	}
	safeName, e1 := nullableKeyString(safeFields["name"])
	safeDescription, e2 := nullableKeyString(safeFields["description"])
	safeActions, e3 := keyStrings(safeFields["actions"])
	safeIndexes, e4 := keyStrings(safeFields["indexes"])
	if e1 != nil || e2 != nil || e3 != nil || e4 != nil || safeName != r.Name || safeDescription != r.Description || !slices.Equal(safeActions, r.Actions) || !slices.Equal(safeIndexes, r.Indexes) {
		return keyEntry{}, ErrProtocol
	}
	r.Token = d.recordProof(r)
	return keyEntry{record: r, secret: []byte(secret)}, nil
}
func (d *keysDocument) original(r contract.KeyRecord) (keyEntry, error) {
	e, ok := d.entries[r.Token]
	if !ok || r.Token == "" || r.Token != d.recordProof(r) || !sameKeyRecord(r, e.record) {
		return keyEntry{}, contract.ErrKeyConflict
	}
	return e, nil
}
func sameKeyRecord(a, b contract.KeyRecord) bool {
	ra, _ := json.Marshal(a)
	rb, _ := json.Marshal(b)
	return bytes.Equal(ra, rb)
}
func (d *keysDocument) store(e keyEntry) error {
	if len(d.entries) >= 256 {
		if _, ok := d.entries[e.record.Token]; !ok {
			clear(e.secret)
			return ErrBounds
		}
	}
	if old, ok := d.entries[e.record.Token]; ok {
		clear(old.secret)
	}
	d.entries[e.record.Token] = e
	return nil
}
func (d *keysDocument) List(ctx context.Context, offset, limit uint64) (contract.KeyPage, error) {
	d.bound.ops.Lock()
	defer d.bound.ops.Unlock()
	if offset > 1<<32 || limit == 0 || limit > 100 {
		return contract.KeyPage{}, ErrBounds
	}
	ctx, done, err := d.request(ctx)
	if err != nil {
		return contract.KeyPage{}, err
	}
	defer done()
	response, err := d.bound.client.Do(ctx, Request{Method: "GET", Path: "/keys?offset=" + strconv.FormatUint(offset, 10) + "&limit=" + strconv.FormatUint(limit, 10)})
	defer clear(response.Body)
	raw := d.publicJSON(response.Body)
	if err != nil {
		return contract.KeyPage{}, d.keyError(err, raw)
	}
	fields, err := nativeObject(response.Body)
	if response.Status != 200 {
		err = ErrProtocol
	}
	if err != nil {
		return contract.KeyPage{}, d.keyError(err, raw)
	}
	o, e1 := nativeUint(fields["offset"], 64)
	l, e2 := nativeUint(fields["limit"], 64)
	total, e3 := nativeUint(fields["total"], 64)
	var rows []json.RawMessage
	if e1 != nil || e2 != nil || e3 != nil || o != offset || l != limit || decodeJSON(fields["results"], &rows) != nil || rows == nil || uint64(len(rows)) > limit || total < uint64(len(rows)) || len(rows) > 0 && (o >= total || uint64(len(rows)) > total-o) {
		return contract.KeyPage{}, d.keyError(ErrProtocol, raw)
	}
	next := map[string]keyEntry{}
	page := contract.KeyPage{Offset: o, Limit: l, Total: total, Raw: raw}
	defer func() {
		for _, entry := range next {
			clear(entry.secret)
		}
	}()
	for _, row := range rows {
		entry, e := d.parse(row)
		if e != nil {
			return contract.KeyPage{}, d.keyError(e, raw)
		}
		if _, ok := next[entry.record.Token]; ok {
			clear(entry.secret)
			return contract.KeyPage{}, d.keyError(ErrProtocol, raw)
		}
		next[entry.record.Token] = entry
		page.Results = append(page.Results, cloneKey(entry.record))
	}
	if saved, ok := d.entries[d.last.Record.Token]; ok {
		if _, exists := next[saved.record.Token]; !exists {
			saved.record = cloneKey(saved.record)
			saved.secret = slices.Clone(saved.secret)
			next[saved.record.Token] = saved
		}
	}
	for _, entry := range d.entries {
		clear(entry.secret)
	}
	d.entries = next
	next = nil
	d.page = cloneKeyPage(page)
	return page, nil
}
func (d *keysDocument) read(ctx context.Context, r contract.KeyRecord) (keyEntry, error) {
	old, err := d.original(r)
	if err != nil {
		return keyEntry{}, err
	}
	response, err := d.bound.client.Do(ctx, Request{Method: "GET", Path: "/keys/" + r.UID})
	defer clear(response.Body)
	raw := d.publicJSON(response.Body)
	if err != nil {
		var status *HTTPError
		if errors.As(err, &status) && status.Status == 404 {
			err = errors.Join(contract.ErrKeyConflict, err)
		}
		return keyEntry{}, d.keyError(err, raw)
	}
	entry, err := d.parse(response.Body)
	if response.Status != 200 {
		clear(entry.secret)
		err = ErrProtocol
	}
	if err != nil {
		return keyEntry{}, d.keyError(err, raw)
	}
	if entry.record.UID != r.UID || entry.record.CreatedAt != r.CreatedAt || !bytes.Equal(old.secret, entry.secret) {
		clear(entry.secret)
		d.bound.client.retire()
		return keyEntry{}, d.keyError(errors.Join(ErrRetired, contract.ErrKeyConflict), raw)
	}
	return entry, nil
}
func (d *keysDocument) Read(ctx context.Context, r contract.KeyRecord) (contract.KeyRecord, error) {
	d.bound.ops.Lock()
	defer d.bound.ops.Unlock()
	if _, err := d.original(r); err != nil {
		return contract.KeyRecord{}, err
	}
	ctx, done, err := d.request(ctx)
	if err != nil {
		return contract.KeyRecord{}, err
	}
	defer done()
	entry, err := d.read(ctx, r)
	if err != nil {
		return contract.KeyRecord{}, err
	}
	if err = d.store(entry); err != nil {
		return contract.KeyRecord{}, err
	}
	return cloneKey(entry.record), nil
}
func (d *keysDocument) Reveal(r contract.KeyRecord) (string, error) {
	d.bound.ops.Lock()
	defer d.bound.ops.Unlock()
	if err := d.alive(); err != nil {
		return "", err
	}
	e, err := d.original(r)
	if err != nil {
		return "", err
	}
	return string(e.secret), nil
}
func keyValues(values []string, action bool) error {
	if len(values) == 0 || len(values) > 256 {
		return ErrInvalid
	}
	seen := map[string]bool{}
	for _, s := range values {
		if s == "" || len(s) > 512 || seen[s] {
			return ErrInvalid
		}
		seen[s] = true
		for _, ch := range s {
			if ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '_' || ch == '-' || ch == '*' || action && ch == '.' {
				continue
			}
			return ErrInvalid
		}
	}
	return nil
}
func keyBody(action string, draft contract.KeyDraft) (string, error) {
	if !utf8.ValidString(draft.Name) || !utf8.ValidString(draft.Description) {
		return "", ErrInvalid
	}
	if len(draft.Name)+len(draft.Description) > keyDraftLimit {
		return "", ErrBounds
	}
	nullable := func(s string) any {
		if s == "" {
			return nil
		}
		return s
	}
	body := map[string]any{"name": nullable(draft.Name), "description": nullable(draft.Description)}
	if action == "create" {
		if draft.UID != "" && !validKeyUID(draft.UID) {
			return "", ErrInvalid
		}
		if err := keyValues(draft.Actions, true); err != nil {
			return "", err
		}
		if err := keyValues(draft.Indexes, false); err != nil {
			return "", err
		}
		if draft.ExpiresAt != "" {
			if len(draft.ExpiresAt) > 64 {
				return "", ErrInvalid
			}
			if _, err := time.Parse(time.RFC3339Nano, draft.ExpiresAt); err != nil {
				return "", ErrInvalid
			}
		}
		body["actions"] = draft.Actions
		body["indexes"] = draft.Indexes
		body["expiresAt"] = nullable(draft.ExpiresAt)
		if draft.UID != "" {
			body["uid"] = draft.UID
		}
	}
	raw, err := json.Marshal(body)
	if err != nil || len(raw) > keyDraftLimit {
		return "", ErrBounds
	}
	return string(raw), nil
}
func (d *keysDocument) Prepare(action string, original contract.KeyRecord, draft contract.KeyDraft) (contract.KeyPlan, error) {
	d.bound.ops.Lock()
	defer d.bound.ops.Unlock()
	if err := d.alive(); err != nil {
		return contract.KeyPlan{}, err
	}
	if d.uncertain {
		return contract.KeyPlan{}, contract.ErrKeyPlanUsed
	}
	plan := contract.KeyPlan{Object: d.object, Action: action, Draft: cloneKeyDraft(draft)}
	switch action {
	case "create":
		plan.Request = "POST /keys"
		if original.Token != "" {
			return contract.KeyPlan{}, ErrInvalid
		}
	case "update", "delete":
		if _, err := d.original(original); err != nil {
			return contract.KeyPlan{}, err
		}
		plan.Original = cloneKey(original)
		plan.Request = "PATCH /keys/" + original.UID
		if action == "delete" {
			plan.Request = "DELETE /keys/" + original.UID
		}
	default:
		return contract.KeyPlan{}, ErrInvalid
	}
	if action != "delete" {
		body, err := keyBody(action, draft)
		if err != nil {
			return contract.KeyPlan{}, err
		}
		plan.Body = body
		if d.publicJSON([]byte(body)) != body {
			return contract.KeyPlan{}, ErrInvalid
		}
	}
	if action == "update" {
		var old, desired map[string]any
		if decodeJSON([]byte(original.Raw), &old) != nil || decodeJSON([]byte(plan.Body), &desired) != nil {
			return contract.KeyPlan{}, ErrInvalid
		}
		a, _ := json.Marshal(map[string]any{"name": old["name"], "description": old["description"]})
		b, _ := json.Marshal(desired)
		if bytes.Equal(a, b) {
			return contract.KeyPlan{}, contract.ErrKeyNoChanges
		}
	}
	d.sequence++
	plan.Sequence = d.sequence
	plan.Token = d.planProof(plan)
	return plan, nil
}
func keyRejected(response Response, err error) bool {
	var status *HTTPError
	if !errors.As(err, &status) || status.Status != response.Status {
		return false
	}
	switch response.Status {
	case 400, 401, 403, 404, 409, 413:
	default:
		return false
	}
	fields, e := nativeObject(response.Body)
	code, e1 := nativeString(fields["code"])
	kind, e2 := nativeString(fields["type"])
	message, e3 := nativeString(fields["message"])
	if e != nil || e1 != nil || e2 != nil || e3 != nil || message == "" || kind != "invalid_request" && kind != "auth" {
		return false
	}
	return strings.HasPrefix(code, "invalid_api_key_") || strings.HasPrefix(code, "immutable_api_key_") || strings.HasPrefix(code, "missing_api_key_") || code == "missing_master_key" || code == "invalid_api_key" || code == "missing_authorization_header" || code == "api_key_not_found" || code == "api_key_already_exists" || code == "payload_too_large"
}
func (d *keysDocument) Apply(ctx context.Context, p contract.KeyPlan) (contract.KeyResult, error) {
	d.bound.ops.Lock()
	defer d.bound.ops.Unlock()
	result := contract.KeyResult{State: "not_submitted"}
	if err := d.alive(); err != nil {
		return result, err
	}
	if p.Token == "" || p.Object != d.object || p.Token != d.planProof(p) || p.Sequence != d.sequence || p.Sequence == d.used || d.uncertain {
		return result, contract.ErrKeyPlanUsed
	}
	d.used = p.Sequence
	result.Action, result.Request = p.Action, p.Request
	if p.Body != "" {
		result.Request += "\n" + p.Body
	}
	result.UID = p.Original.UID
	if p.Action != "create" {
		if _, err := d.original(p.Original); err != nil {
			return result, err
		}
	}
	ctx, done, err := d.request(ctx)
	if err != nil {
		return result, err
	}
	defer done()
	if p.Action != "create" {
		entry, e := d.read(ctx, p.Original)
		if e != nil {
			return result, e
		}
		defer clear(entry.secret)
		if entry.record.Fingerprint != p.Original.Fingerprint {
			return result, contract.ErrKeyConflict
		}
	}
	if err = ctx.Err(); err != nil {
		return result, err
	}
	method, path, _ := strings.Cut(p.Request, " ")
	request := Request{Method: method, Path: path, Body: []byte(p.Body)}
	if _, err = url.ParseRequestURI(path); err != nil {
		return result, ErrInvalid
	}
	response, err := d.bound.client.Do(ctx, request)
	defer clear(response.Body)
	result.Status, result.Response = response.Status, d.publicJSON(response.Body)
	if err != nil {
		var interrupted *Interrupted
		if keyRejected(response, err) {
			result.State = "rejected"
		} else if errors.As(err, &interrupted) && interrupted.WriteOutcomeUnknown {
			result.State = "uncertain"
			d.uncertain = true
		}
		d.last = cloneKeyResult(result)
		return result, d.keyError(err, result.Response)
	}
	expected := 201
	if p.Action == "update" {
		expected = 200
	}
	if p.Action == "delete" {
		expected = 204
	}
	valid := response.Status == expected
	if p.Action == "delete" {
		valid = valid && len(response.Body) == 0
		if valid {
			result.Response = ""
			for token, e := range d.entries {
				if e.record.UID == p.Original.UID {
					clear(e.secret)
					delete(d.entries, token)
				}
			}
		}
	} else if valid {
		entry, e := d.parse(response.Body)
		if e != nil {
			valid = false
		} else {
			originalEntry := d.entries[p.Original.Token]
			if p.Action == "create" && (p.Draft.UID != "" && entry.record.UID != p.Draft.UID || !sameKeySet(entry.record.Actions, p.Draft.Actions) || !sameKeySet(entry.record.Indexes, p.Draft.Indexes) || !sameKeyExpiry(entry.record.ExpiresAt, p.Draft.ExpiresAt)) || p.Action == "update" && (entry.record.UID != p.Original.UID || entry.record.CreatedAt != p.Original.CreatedAt || !bytes.Equal(entry.secret, originalEntry.secret) || !sameKeySet(entry.record.Actions, p.Original.Actions) || !sameKeySet(entry.record.Indexes, p.Original.Indexes) || !sameKeyExpiry(entry.record.ExpiresAt, p.Original.ExpiresAt)) || entry.record.Name != p.Draft.Name || entry.record.Description != p.Draft.Description {
				valid = false
				clear(entry.secret)
			} else if err = d.store(entry); err != nil {
				valid = false
			} else {
				result.Record = cloneKey(entry.record)
				result.UID = entry.record.UID
			}
		}
	}
	if !valid {
		result.State = "uncertain"
		d.uncertain = true
		d.bound.client.retire()
		d.last = cloneKeyResult(result)
		return result, d.keyError(&Interrupted{WriteOutcomeUnknown: true, Cause: ErrProtocol}, result.Response)
	}
	result.State = "completed"
	d.last = cloneKeyResult(result)
	return result, nil
}
func (s *Session) RecoverKeysDocument(o contract.ObjectRef, previous contract.KeysDocumentLease) (contract.KeyRecovery, error) {
	old, ok := previous.(*keysDocument)
	if !ok || old.object != o || old.bound.s.profileID != s.profileID || old.bound.s.config.Endpoint != s.config.Endpoint || old.bound.s.config.IndexUID != s.config.IndexUID {
		return contract.KeyRecovery{}, ErrInvalid
	}
	lease, err := s.ReserveKeysDocument(o)
	if err != nil {
		return contract.KeyRecovery{}, err
	}
	next := lease.(*keysDocument)
	old.bound.Release()
	old.bound.ops.Lock()
	next.uncertain = old.uncertain
	next.last = cloneKeyResult(old.last)
	next.page = cloneKeyPage(old.page)
	tokens := map[string]contract.KeyRecord{}
	for token, e := range old.entries {
		e.record = cloneKey(e.record)
		e.secret = slices.Clone(e.secret)
		e.record.Token = next.recordProof(e.record)
		next.entries[e.record.Token] = e
		tokens[token] = e.record
	}
	for i, r := range next.page.Results {
		if updated, ok := tokens[r.Token]; ok {
			next.page.Results[i] = cloneKey(updated)
		}
	}
	if r, ok := tokens[next.last.Record.Token]; ok {
		next.last.Record = cloneKey(r)
	}
	old.bound.ops.Unlock()
	_ = old.Close()
	return contract.KeyRecovery{Lease: next, Page: cloneKeyPage(next.page), Last: cloneKeyResult(next.last), Uncertain: next.uncertain}, nil
}
func (d *keysDocument) Close() error {
	d.bound.Release()
	d.bound.ops.Lock()
	defer d.bound.ops.Unlock()
	for _, e := range d.entries {
		clear(e.secret)
	}
	clear(d.entries)
	clear(d.proof.key[:])
	d.page = contract.KeyPage{}
	d.last = contract.KeyResult{}
	return nil
}

var _ contract.KeysDocumentsProvider = (*Session)(nil)

func sameKeySet(a, b []string) bool {
	a = slices.Clone(a)
	b = slices.Clone(b)
	slices.Sort(a)
	slices.Sort(b)
	return slices.Equal(a, b)
}
func sameKeyExpiry(a, b string) bool {
	if a == "" || b == "" {
		return a == b
	}
	x, e1 := time.Parse(time.RFC3339Nano, a)
	y, e2 := time.Parse(time.RFC3339Nano, b)
	return e1 == nil && e2 == nil && x.Equal(y)
}
