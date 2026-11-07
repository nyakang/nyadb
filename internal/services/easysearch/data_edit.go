package easysearch

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"

	"github.com/nyakang/nyadb/internal/db/contract"
)

type dataTarget struct {
	members                         map[string]string
	insertIndex, routing, signature string
	customRouting, editable         bool
}
type preparedDocumentEdit struct {
	kind     string
	original *contract.RowSnapshot
	identity rowIdentity
	version  rowVersion
	source   json.RawMessage
}
type pendingBatch struct {
	plans           []preparedDocumentEdit
	result          *contract.DataEditResult
	receipt         string
	targetSignature string
	attempted       int
	unsafeIdentity  bool
}

func indexUUID(ctx context.Context, c *Client, index string) (string, error) {
	r, err := c.Do(ctx, Request{Method: "GET", Path: "/" + url.PathEscape(index) + "/_settings/index.uuid?flat_settings=true"})
	if err != nil {
		return "", err
	}
	uuids, err := metadataIndexUUIDs(r.Body, []string{index})
	if err != nil {
		return "", err
	}
	return uuids[index], nil
}

func (l *dataLease) dataTarget(ctx context.Context, c *Client) (dataTarget, error) {
	out := dataTarget{members: map[string]string{}}
	binding, err := l.guard(ctx, c)
	if err != nil {
		return out, err
	}
	target := binding.target
	out.editable = !strings.HasPrefix(target.Name, ".") && !slices.Contains(target.Attributes, "hidden") && !slices.Contains(target.Attributes, "system") && !slices.Contains(target.Attributes, "closed")
	for member, uuid := range binding.uuids {
		out.members[member] = uuid
		resolved, e := c.ResolveTargets(ctx, member)
		if e != nil {
			return out, e
		}
		found := false
		for _, concrete := range resolved {
			if concrete.Kind == "index" && concrete.Name == member {
				found = true
				if !slices.Contains(concrete.Attributes, "open") || slices.Contains(concrete.Attributes, "system") || slices.Contains(concrete.Attributes, "closed") || l.object.Kind != "data_stream" && (strings.HasPrefix(member, ".") || slices.Contains(concrete.Attributes, "hidden")) {
					out.editable = false
				}
			}
		}
		if !found {
			return out, ErrTargetChanged
		}
	}
	// A filtered or disabled _source is insufficient for replacing a complete
	// document. Native dynamic properties are excluded from this signature:
	// a successful create may legitimately add those properties itself.
	r, err := c.Do(ctx, Request{Method: "GET", Path: "/" + url.PathEscape(l.object.Name) + "/_mapping"})
	if err != nil {
		return out, err
	}
	var mappings map[string]struct {
		Mappings *struct {
			Source json.RawMessage `json:"_source"`
		} `json:"mappings"`
	}
	if decodeJSON(r.Body, &mappings) != nil || len(mappings) != len(out.members) {
		return out, ErrProtocol
	}
	sources := map[string]json.RawMessage{}
	for member := range out.members {
		m := mappings[member].Mappings
		if m == nil {
			return out, ErrProtocol
		}
		if len(m.Source) > 0 {
			var source struct {
				Enabled                  *bool `json:"enabled"`
				Includes, Excludes, Mode json.RawMessage
			}
			if decodeJSON(m.Source, &source) != nil {
				return out, ErrProtocol
			}
			if source.Enabled != nil && !*source.Enabled || hasSourceFilter(source.Includes) || hasSourceFilter(source.Excludes) || len(source.Mode) > 0 {
				out.editable = false
			}
		}
		sources[member] = bytes.Clone(m.Source)
	}
	switch l.object.Kind {
	case "index":
		out.insertIndex, out.customRouting = l.object.Name, true
	case "alias":
		var aliases map[string]json.RawMessage
		if decodeJSON(binding.scope, &aliases) != nil || len(aliases) != len(out.members) {
			return out, ErrProtocol
		}
		for member, raw := range aliases {
			var a struct {
				Write        *bool  `json:"is_write_index"`
				Hidden       bool   `json:"is_hidden"`
				Routing      string `json:"routing"`
				IndexRouting string `json:"index_routing"`
			}
			if decodeJSON(raw, &a) != nil || out.members[member] == "" {
				return out, ErrProtocol
			}
			if a.Hidden {
				out.editable = false
			}
			if a.Write != nil && *a.Write || len(aliases) == 1 && a.Write == nil {
				if out.insertIndex != "" {
					return out, ErrProtocol
				}
				out.insertIndex, out.routing = member, a.IndexRouting
				if out.routing == "" {
					out.routing = a.Routing
				}
			}
		}
		out.customRouting = true
		if out.insertIndex == "" {
			out.editable = false
		}
	case "data_stream":
		var root struct {
			Streams []struct {
				Name           string `json:"name"`
				Hidden, System bool
				Custom         bool `json:"allow_custom_routing"`
				Indices        []struct {
					Name string `json:"index_name"`
					UUID string `json:"index_uuid"`
				} `json:"indices"`
			} `json:"data_streams"`
		}
		if decodeJSON(binding.scope, &root) != nil || len(root.Streams) != 1 || root.Streams[0].Name != l.object.Name || len(root.Streams[0].Indices) == 0 {
			return out, ErrProtocol
		}
		stream := root.Streams[0]
		out.editable = out.editable && !stream.Hidden && !stream.System
		out.customRouting = stream.Custom
		last := stream.Indices[len(stream.Indices)-1]
		if out.members[last.Name] != last.UUID {
			return out, ErrTargetChanged
		}
		out.insertIndex = last.Name
	default:
		return out, contract.ErrUnsupported
	}
	if _, err = l.guard(ctx, c); err != nil {
		return out, err
	}
	out.signature = l.sign(struct {
		Members          map[string]string
		Scope            json.RawMessage
		Sources          map[string]json.RawMessage
		Index, Routing   string
		Custom, Editable bool
	}{out.members, binding.scope, sources, out.insertIndex, out.routing, out.customRouting, out.editable})
	return out, nil
}

func hasSourceFilter(raw json.RawMessage) bool {
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return false
	}
	var fields []string
	if json.Unmarshal(raw, &fields) == nil {
		return len(fields) != 0
	}
	var field string
	return json.Unmarshal(raw, &field) != nil || field != ""
}

func cloneDocumentSnapshot(s *contract.RowSnapshot) *contract.RowSnapshot {
	if s == nil {
		return nil
	}
	out := *s
	out.Ref.Columns = append([]string(nil), s.Ref.Columns...)
	out.Ref.Values = nil
	for _, v := range s.Ref.Values {
		out.Ref.Values = append(out.Ref.Values, bytes.Clone(v))
	}
	out.Ref.TypedValues = nil
	for _, v := range s.Ref.TypedValues {
		out.Ref.TypedValues = append(out.Ref.TypedValues, v.Clone())
	}
	out.Columns = append([]string(nil), s.Columns...)
	out.Original = nil
	for _, v := range s.Original {
		out.Original = append(out.Original, v.Clone())
	}
	out.Fingerprints = nil
	for _, v := range s.Fingerprints {
		if v == nil {
			out.Fingerprints = append(out.Fingerprints, nil)
		} else {
			copy := *v
			out.Fingerprints = append(out.Fingerprints, &copy)
		}
	}
	return &out
}
func cloneDocumentResult(r *contract.DataEditResult) *contract.DataEditResult {
	if r == nil {
		return nil
	}
	out := *r
	out.Outcomes = nil
	for _, v := range r.Outcomes {
		v.Original = cloneDocumentSnapshot(v.Original)
		v.Saved = cloneDocumentSnapshot(v.Saved)
		v.PreviewColumns = append([]string(nil), v.PreviewColumns...)
		previews := append([]contract.DataCell(nil), v.Previews...)
		for i := range previews {
			if previews[i].Value != nil {
				copy := previews[i].Value.Clone()
				previews[i].Value = &copy
			}
		}
		v.Previews = previews
		out.Outcomes = append(out.Outcomes, v)
	}
	return &out
}
func (l *dataLease) sealSnapshot(s *contract.RowSnapshot, v rowVersion) {
	s.Metadata = ""
	v.Signature = ""
	v.Signature = l.sign(struct {
		Snapshot *contract.RowSnapshot
		Version  rowVersion
	}{s, v})
	raw, _ := json.Marshal(v)
	s.Metadata = string(raw)
}
func (l *dataLease) intentSnapshot(db string, s Snapshot, uuid string) *contract.RowSnapshot {
	out := l.snapshot(db, s, uuid)
	out.Mode = "intent"
	l.sealSnapshot(out, rowVersion{Sequence: s.Sequence, PrimaryTerm: s.PrimaryTerm})
	return out
}
func (l *dataLease) validateSnapshot(db string, s *contract.RowSnapshot, allowIntent bool) (rowIdentity, rowVersion, error) {
	var identity rowIdentity
	var version rowVersion
	if s == nil || !slices.Equal(s.Columns, documentFields()) || decodeJSON([]byte(s.Metadata), &version) != nil {
		return identity, version, ErrInvalid
	}
	var err error
	identity, err = l.ref(db, s.Ref)
	if err != nil {
		return identity, version, err
	}
	if version.Sequence < 0 || version.PrimaryTerm <= 0 {
		if !allowIntent || s.Mode != "intent" || version.Sequence != -1 || version.PrimaryTerm != -1 {
			return identity, version, ErrInvalid
		}
	}
	switch s.Mode {
	case "fingerprints", "intent":
		if s.Mode == "intent" && !allowIntent || len(s.Original) != 0 || len(s.Fingerprints) != 4 {
			return identity, version, ErrInvalid
		}
		for _, fp := range s.Fingerprints {
			if fp == nil || len(*fp) != 64 {
				return identity, version, ErrInvalid
			}
		}
	case "values":
		if len(s.Fingerprints) != 0 || len(s.Original) != 4 {
			return identity, version, ErrInvalid
		}
		for i, cell := range s.Original {
			kind := contract.Text
			if i == 3 {
				kind = contract.JSON
			}
			if cell.Kind != kind || cell.Truncated || len(cell.Bytes) != 0 || i == 3 && !validSource([]byte(cell.Text)) {
				return identity, version, ErrInvalid
			}
		}
	default:
		return identity, version, ErrInvalid
	}
	copy := *s
	copy.Metadata = ""
	signature := version.Signature
	version.Signature = ""
	expected := l.sign(struct {
		Snapshot *contract.RowSnapshot
		Version  rowVersion
	}{&copy, version})
	version.Signature = signature
	if !hmac.Equal([]byte(signature), []byte(expected)) {
		return identity, version, ErrInvalid
	}
	return identity, version, nil
}
func matchesDocumentSnapshot(document Snapshot, s *contract.RowSnapshot) bool {
	if s == nil {
		return false
	}
	if s.Mode == "values" {
		if len(s.Original) != 4 {
			return false
		}
		cells := documentCells(document)
		for i, c := range cells {
			if i == 3 {
				if sourceFingerprint([]byte(c.Text)) != sourceFingerprint([]byte(s.Original[i].Text)) {
					return false
				}
			} else if c.Text != s.Original[i].Text || c.Kind != s.Original[i].Kind {
				return false
			}
		}
		return true
	}
	if len(s.Fingerprints) != 4 {
		return false
	}
	for i, c := range documentCells(document) {
		fp := sourceFingerprint(document.Source)
		if i != 3 {
			sum := sha256.Sum256([]byte(c.Text))
			fp = hex.EncodeToString(sum[:])
		}
		if s.Fingerprints[i] == nil || *s.Fingerprints[i] != fp {
			return false
		}
	}
	return true
}
func textDataValue(v contract.DataValue) (string, error) {
	if v.Null || v.Default || v.Binary || len(v.Bytes) != 0 || v.Kind != "" && v.Kind != contract.Text {
		return "", ErrInvalid
	}
	return v.Text, nil
}
func (l *dataLease) prepareEdits(db string, edits []contract.DataEdit) ([]preparedDocumentEdit, error) {
	if len(edits) == 0 || len(edits) > contract.DataMaxPageSize {
		return nil, ErrBounds
	}
	prepared := make([]preparedDocumentEdit, 0, len(edits))
	size := 0
	for _, edit := range edits {
		p := preparedDocumentEdit{kind: edit.Kind}
		if edit.Kind != "insert" && edit.Kind != "update" && edit.Kind != "delete" {
			return nil, contract.ErrUnsupported
		}
		if edit.Kind == "insert" {
			if edit.Original != nil {
				return nil, ErrInvalid
			}
			var random [16]byte
			if _, err := rand.Read(random[:]); err != nil {
				return nil, err
			}
			p.identity = rowIdentity{Object: l.object, Database: db, ID: DocumentID{ID: "nya_" + hex.EncodeToString(random[:])}}
		} else {
			var err error
			p.original = cloneDocumentSnapshot(edit.Original)
			p.identity, p.version, err = l.validateSnapshot(db, p.original, false)
			if err != nil {
				return nil, err
			}
		}
		if edit.Kind == "delete" {
			if len(edit.Values) != 0 {
				return nil, ErrInvalid
			}
		} else {
			value, ok := edit.Values["document"]
			if !ok || value.Null || value.Default || value.Binary || len(value.Bytes) != 0 || value.Kind != "" && value.Kind != contract.JSON && value.Kind != contract.Text || !validSource([]byte(value.Text)) {
				return nil, errors.New("edit one complete Easysearch JSON document within 1 MiB")
			}
			p.source = json.RawMessage(value.Text)
			for field, v := range edit.Values {
				switch field {
				case "document":
				case "_routing":
					if edit.Kind == "insert" && (v.Null || v.Default) {
						continue
					}
					text, err := textDataValue(v)
					if err != nil {
						return nil, err
					}
					if edit.Kind != "insert" && text != p.identity.ID.Routing {
						return nil, errors.New("Easysearch document routing is immutable")
					}
					p.identity.ID.Routing = text
				case "_id":
					if edit.Kind != "insert" {
						return nil, ErrInvalid
					}
					text, err := textDataValue(v)
					if err != nil {
						return nil, err
					}
					p.identity.ID.ID = text
				default:
					return nil, contract.ErrUnsupported
				}
			}
		}
		raw, _ := json.Marshal(edit)
		size += len(raw)
		if size > 16<<20 {
			return nil, ErrBounds
		}
		prepared = append(prepared, p)
	}
	return prepared, nil
}

func (l *dataLease) save(ctx context.Context, id int64, db, name string, edits []contract.DataEdit) (*contract.DataEditResult, error) {
	if err := l.identity(id, db, name); err != nil {
		return nil, err
	}
	ctx, done, err := l.request(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	if !l.editMu.TryLock() {
		return nil, ErrBusy
	}
	defer l.editMu.Unlock()
	if l.pending != nil {
		return nil, contract.ErrDataCommitUncertain
	}
	plans, err := l.prepareEdits(db, edits)
	if err != nil {
		return nil, err
	}
	c, err := l.session.open(ctx)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	l.mu.Lock()
	l.writing = true
	l.mu.Unlock()
	defer func() { l.mu.Lock(); l.writing = false; l.mu.Unlock() }()
	target, err := l.dataTarget(ctx, c)
	if err != nil {
		return nil, err
	}
	if !target.editable {
		return nil, contract.ErrDataReadOnly
	}
	seen := map[DocumentID]bool{}
	for i := range plans {
		p := &plans[i]
		if p.kind == "insert" {
			if target.insertIndex == "" {
				return nil, errors.New("Easysearch target has no unambiguous write index")
			}
			p.identity.ID.Index = target.insertIndex
			p.identity.UUID = target.members[target.insertIndex]
			if target.routing != "" {
				if p.identity.ID.Routing != "" && p.identity.ID.Routing != target.routing {
					return nil, ErrInvalid
				}
				p.identity.ID.Routing = target.routing
			}
			if !target.customRouting && p.identity.ID.Routing != "" {
				return nil, contract.ErrUnsupported
			}
		}
		if target.members[p.identity.ID.Index] != p.identity.UUID || !validDocumentID(p.identity.ID) {
			return nil, contract.ErrDataConflict
		}
		if seen[p.identity.ID] {
			return nil, ErrInvalid
		}
		seen[p.identity.ID] = true
	}
	result := &contract.DataEditResult{}
	batch := &pendingBatch{plans: plans, result: result, targetSignature: target.signature}
	defer func() {
		if !result.CommitConfirmed && (result.CommitUncertain || result.Applied > 0) {
			result.Incomplete = result.Incomplete || batch.attempted < len(plans) || !result.CommitUncertain && result.Applied > 0
			batch.result = cloneDocumentResult(result)
			batch.receipt = l.sign(result)
			l.pending = batch
		}
	}()
	for i, p := range plans {
		currentTarget, e := l.dataTarget(ctx, c)
		if e != nil {
			return result, e
		}
		if currentTarget.signature != target.signature {
			return result, contract.ErrDataConflict
		}
		var original Snapshot
		if p.kind == "insert" {
			_, e = c.GetDocument(ctx, p.identity.ID)
			if e == nil {
				return result, contract.ErrDataConflict
			}
			if !isNativeStatus(e, 404) {
				return result, e
			}
			if _, e = l.guard(ctx, c); e != nil {
				return result, e
			}
		}
		if p.kind != "insert" {
			if l.object.Kind != "index" {
				if _, e = l.read(ctx, c, p.identity); e != nil {
					return result, e
				}
			}
			original, e = c.GetDocument(ctx, p.identity.ID)
			if e != nil {
				return result, e
			}
			if original.Sequence != p.version.Sequence || original.PrimaryTerm != p.version.PrimaryTerm || !matchesDocumentSnapshot(original, p.original) {
				return result, fmt.Errorf("%w: Easysearch document changed", contract.ErrDataConflict)
			}
		}
		// Recheck the whole frozen selector after reading the original row.
		// A concrete GET alone cannot protect an alias scope or a stream rollover.
		currentTarget, e = l.dataTarget(ctx, c)
		if e != nil {
			return result, e
		}
		if currentTarget.signature != target.signature {
			return result, contract.ErrDataConflict
		}
		outcome := contract.DataEditOutcome{Kind: p.kind, Original: cloneDocumentSnapshot(p.original), PreviewColumns: documentFields()}
		intent := Snapshot{Identity: p.identity.ID, Sequence: -1, PrimaryTerm: -1, Source: p.source}
		if p.kind != "delete" {
			outcome.Saved = l.intentSnapshot(db, intent, p.identity.UUID)
			outcome.Previews = documentPreviews(intent)
		}
		batch.attempted = i + 1
		options := mutationOptions{WaitRefresh: true}
		var mutation MutationResult
		switch p.kind {
		case "insert":
			options.Target = l.object.Name
			options.RequireAlias = l.object.Kind == "alias"
			mutation, e = c.mutateDocument(ctx, "PUT", "_create", p.identity.ID, nil, p.source, options, "created")
		case "update":
			mutation, e = c.mutateDocument(ctx, "PUT", "_doc", p.identity.ID, &original, p.source, options, "updated", "noop")
		case "delete":
			mutation, e = c.mutateDocument(ctx, "DELETE", "_doc", p.identity.ID, &original, nil, options, "deleted")
		}
		if e != nil {
			var interrupted *Interrupted
			result.CommitUncertain = errors.As(e, &interrupted) && interrupted.WriteOutcomeUnknown
			if result.CommitUncertain {
				result.RefreshRequired = true
				l.markDirty()
				result.Outcomes = append(result.Outcomes, outcome)
				result.Incomplete = i+1 < len(plans)
				var native struct {
					Index string `json:"_index"`
				}
				if decodeJSON(mutation.Response.Body, &native) == nil && native.Index != "" && native.Index != p.identity.ID.Index {
					batch.unsafeIdentity = true
				}
				return result, errors.Join(contract.ErrDataCommitUncertain, e)
			}
			if isNativeStatus(e, 409) {
				return result, errors.Join(contract.ErrDataConflict, e)
			}
			return result, e
		}
		result.Applied++
		result.RefreshRequired = true
		l.markDirty()
		if p.kind != "delete" {
			intent.Sequence, intent.PrimaryTerm = mutation.Snapshot.Sequence, mutation.Snapshot.PrimaryTerm
			outcome.Saved = l.intentSnapshot(db, intent, p.identity.UUID)
		}
		postTarget, e := l.dataTarget(ctx, c)
		if e != nil || postTarget.signature != target.signature {
			batch.unsafeIdentity = errors.Is(e, ErrTargetChanged) || e == nil
			result.CommitUncertain = true
			result.Incomplete = i+1 < len(plans)
			result.Outcomes = append(result.Outcomes, outcome)
			return result, errors.Join(contract.ErrDataCommitUncertain, e)
		}
		if p.kind != "delete" {
			saved, e := c.GetDocument(ctx, p.identity.ID)
			if e != nil || saved.Sequence != mutation.Snapshot.Sequence || saved.PrimaryTerm != mutation.Snapshot.PrimaryTerm {
				result.CommitUncertain = true
				result.Incomplete = i+1 < len(plans)
				result.Outcomes = append(result.Outcomes, outcome)
				return result, errors.Join(contract.ErrDataCommitUncertain, e)
			}
			finalUUID, e := indexUUID(ctx, c, p.identity.ID.Index)
			if e != nil || finalUUID != p.identity.UUID {
				batch.unsafeIdentity = true
				result.CommitUncertain = true
				result.Incomplete = i+1 < len(plans)
				result.Outcomes = append(result.Outcomes, outcome)
				return result, errors.Join(contract.ErrDataCommitUncertain, e)
			}
			outcome.Saved = l.snapshot(db, saved, p.identity.UUID)
			outcome.Previews = documentPreviews(saved)
		} else {
			_, e = c.GetDocument(ctx, p.identity.ID)
			if !isNativeStatus(e, 404) {
				result.CommitUncertain = true
				result.Incomplete = i+1 < len(plans)
				result.Outcomes = append(result.Outcomes, outcome)
				return result, errors.Join(contract.ErrDataCommitUncertain, e)
			}
		}
		// The final source/deletion read must still belong to the full original
		// binding, including alias filter/routing and every stream backing UUID.
		finalTarget, e := l.dataTarget(ctx, c)
		if e != nil || finalTarget.signature != target.signature {
			batch.unsafeIdentity = errors.Is(e, ErrTargetChanged) || e == nil
			result.CommitUncertain = true
			result.Incomplete = i+1 < len(plans)
			result.Outcomes = append(result.Outcomes, outcome)
			return result, errors.Join(contract.ErrDataCommitUncertain, e)
		}
		result.Outcomes = append(result.Outcomes, outcome)
	}
	result.CommitConfirmed = true
	return result, nil
}
func isNativeStatus(err error, status int) bool {
	var native *HTTPError
	return errors.As(err, &native) && native.Status == status
}

func (l *dataLease) verify(ctx context.Context, id int64, db, name string, result *contract.DataEditResult) (*contract.DataVerification, error) {
	if err := l.identity(id, db, name); err != nil {
		return nil, err
	}
	ctx, done, err := l.request(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	if !l.editMu.TryLock() {
		return nil, ErrBusy
	}
	defer l.editMu.Unlock()
	batch := l.pending
	if batch == nil || result == nil || !hmac.Equal([]byte(batch.receipt), []byte(l.sign(result))) {
		return nil, ErrInvalid
	}
	c, err := l.session.open(ctx)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	verification := &contract.DataVerification{Status: "indeterminate"}
	recovered := cloneDocumentResult(batch.result)
	allApplied := true
	for i, p := range batch.plans {
		state := "indeterminate"
		if i >= batch.attempted {
			verification.States = append(verification.States, "not_processed")
			allApplied = false
			continue
		}
		binding, e := l.dataTarget(ctx, c)
		if errors.Is(e, ErrTargetChanged) || e == nil && (binding.members[p.identity.ID.Index] != p.identity.UUID || binding.signature != batch.targetSignature) || batch.unsafeIdentity {
			verification.States = append(verification.States, state)
			allApplied = false
			continue
		}
		if e != nil {
			return nil, e
		}
		document, e := c.GetDocument(ctx, p.identity.ID)
		if e != nil && !isNativeStatus(e, 404) {
			return nil, e
		}
		var outcome *contract.DataEditOutcome
		if i < len(recovered.Outcomes) {
			outcome = &recovered.Outcomes[i]
		}
		if p.kind == "delete" && isNativeStatus(e, 404) {
			state = "applied"
		} else if p.kind == "insert" && isNativeStatus(e, 404) {
			state = "not_applied"
		} else if e == nil {
			originalMatches := p.original != nil && document.Sequence == p.version.Sequence && document.PrimaryTerm == p.version.PrimaryTerm && matchesDocumentSnapshot(document, p.original)
			if originalMatches {
				state = "not_applied"
			} else if p.kind != "delete" && outcome != nil && outcome.Saved != nil {
				_, known, check := l.validateSnapshot(db, outcome.Saved, true)
				if check != nil {
					return nil, check
				}
				// A known native receipt can correlate a subsequent real source
				// read, including pipeline transformations. Without its tokens,
				// only the captured desired state may be observed; no replay.
				matches := known.Sequence >= 0 && known.PrimaryTerm > 0 && document.Sequence == known.Sequence && document.PrimaryTerm == known.PrimaryTerm
				if known.Sequence == -1 && known.PrimaryTerm == -1 {
					matches = matchesDocumentSnapshot(document, outcome.Saved) && (p.kind == "insert" || document.Sequence > p.version.Sequence)
				}
				if matches {
					state = "applied"
					outcome.Saved = l.snapshot(db, document, p.identity.UUID)
					outcome.Previews = documentPreviews(document)
				}
			}
		}
		post, e := l.dataTarget(ctx, c)
		if errors.Is(e, ErrTargetChanged) {
			state = "indeterminate"
		} else if e != nil {
			return nil, e
		} else if post.members[p.identity.ID.Index] != p.identity.UUID || post.signature != batch.targetSignature {
			state = "indeterminate"
		}
		verification.States = append(verification.States, state)
		allApplied = allApplied && state == "applied"
	}
	if allApplied && !batch.result.Incomplete && len(recovered.Outcomes) == len(batch.plans) {
		verification.Status = "applied"
		recovered.CommitConfirmed, recovered.CommitUncertain, recovered.Verified = true, false, true
		recovered.Applied = len(batch.plans)
		verification.Recovered = recovered
		l.pending = nil
	}
	return verification, nil
}

func (l *dataLease) markDirty() {
	l.mu.Lock()
	l.revision++
	l.dirty = true
	l.mu.Unlock()
}
