package solr

import (
	"bytes"
	"context"
	"crypto/hmac"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
)

type solrEditPlan struct {
	kind     string
	original *contract.RowSnapshot
	identity projectionIdentity
	desired  map[string]json.RawMessage
	mutation NativeDocumentMutation
	receipt  *NativeDocumentReceipt
}
type pendingSolrBatch struct {
	plans            []solrEditPlan
	attempted        int
	result           *contract.DataEditResult
	receipt, binding string
}

func writableDataColumns() []contract.StructureColumn {
	columns := dataColumns()
	for _, i := range []int{0, 1, 4} {
		columns[i].Generated = false
	}
	columns[4].Comment = "Edit visible fields as JSON; identity, version and copy destinations are immutable. Inserts require the native unique key."
	return columns
}
func (l *dataLease) sealEditSnapshot(s *contract.RowSnapshot) {
	s.Metadata = ""
	s.Metadata = l.sign(s)
}
func cloneSolrSnapshot(s *contract.RowSnapshot) *contract.RowSnapshot {
	if s == nil {
		return nil
	}
	copy := *s
	copy.Ref.Columns = slices.Clone(s.Ref.Columns)
	copy.Ref.Values = nil
	for _, v := range s.Ref.Values {
		copy.Ref.Values = append(copy.Ref.Values, bytes.Clone(v))
	}
	copy.Ref.TypedValues = nil
	for _, v := range s.Ref.TypedValues {
		copy.Ref.TypedValues = append(copy.Ref.TypedValues, v.Clone())
	}
	copy.Columns = slices.Clone(s.Columns)
	copy.Original = nil
	for _, v := range s.Original {
		copy.Original = append(copy.Original, v.Clone())
	}
	copy.Fingerprints = nil
	for _, f := range s.Fingerprints {
		if f == nil {
			copy.Fingerprints = append(copy.Fingerprints, nil)
		} else {
			v := *f
			copy.Fingerprints = append(copy.Fingerprints, &v)
		}
	}
	return &copy
}
func cloneSolrResult(r *contract.DataEditResult) *contract.DataEditResult {
	if r == nil {
		return nil
	}
	copy := *r
	copy.Outcomes = nil
	for _, o := range r.Outcomes {
		o.Original = cloneSolrSnapshot(o.Original)
		o.Saved = cloneSolrSnapshot(o.Saved)
		o.PreviewColumns = slices.Clone(o.PreviewColumns)
		o.Previews = slices.Clone(o.Previews)
		for i := range o.Previews {
			if o.Previews[i].Value != nil {
				v := o.Previews[i].Value.Clone()
				o.Previews[i].Value = &v
			}
		}
		copy.Outcomes = append(copy.Outcomes, o)
	}
	copy.Saved = nil
	return &copy
}
func (l *dataLease) validateEditSnapshot(db string, s *contract.RowSnapshot) (projectionIdentity, error) {
	if s == nil || !slices.Equal(s.Columns, dataFields()) {
		return projectionIdentity{}, ErrInvalid
	}
	copy := *s
	copy.Metadata = ""
	if !hmac.Equal([]byte(s.Metadata), []byte(l.sign(&copy))) {
		return projectionIdentity{}, ErrInvalid
	}
	if s.Mode == "values" {
		if len(s.Original) != 5 || len(s.Fingerprints) != 0 {
			return projectionIdentity{}, ErrInvalid
		}
	} else if s.Mode == "fingerprints" {
		if len(s.Fingerprints) != 5 || len(s.Original) != 0 {
			return projectionIdentity{}, ErrInvalid
		}
	} else {
		return projectionIdentity{}, ErrInvalid
	}
	return l.ref(db, s.Ref)
}
func matchesSolrSnapshot(v unitProjection, s *contract.RowSnapshot) bool {
	for i, c := range projectionCells(v) {
		if s.Mode == "values" {
			if c.Kind != s.Original[i].Kind || c.Text != s.Original[i].Text {
				return false
			}
		} else if s.Fingerprints[i] == nil || *s.Fingerprints[i] != fingerprint([]byte(c.Text)) {
			return false
		}
	}
	return true
}
func completeJSON(text string) (map[string]json.RawMessage, error) {
	if len(text) > 1<<20 {
		return nil, ErrBounds
	}
	var fields map[string]json.RawMessage
	if decodeJSON([]byte(text), &fields) != nil || fields == nil || len(fields) > 4096 {
		return nil, ErrInvalid
	}
	for name, value := range fields {
		if !nativeFieldName.MatchString(name) || !flatNativeValue(value) {
			return nil, contract.ErrUnsupported
		}
	}
	return fields, nil
}
func (l *dataLease) prepareSolrEdits(db string, edits []contract.DataEdit) ([]solrEditPlan, error) {
	if len(edits) == 0 || len(edits) > contract.DataMaxPageSize {
		return nil, ErrBounds
	}
	plans := make([]solrEditPlan, 0, len(edits))
	total := 0
	for _, edit := range edits {
		raw, e := json.Marshal(edit)
		if e != nil || len(raw) > 16<<20-total {
			return nil, ErrBounds
		}
		total += len(raw)
		p := solrEditPlan{kind: edit.Kind, original: cloneSolrSnapshot(edit.Original)}
		if p.kind != "insert" && p.kind != "update" && p.kind != "delete" {
			return nil, contract.ErrUnsupported
		}
		if p.kind == "insert" {
			if p.original != nil {
				return nil, ErrInvalid
			}
		} else {
			var err error
			p.identity, err = l.validateEditSnapshot(db, p.original)
			if err != nil {
				return nil, err
			}
		}
		if p.kind == "delete" {
			if len(edit.Values) != 0 {
				return nil, ErrInvalid
			}
		} else {
			v, ok := edit.Values["document"]
			if !ok || v.Null || v.Default || v.Binary || len(v.Bytes) != 0 || v.Kind != "" && v.Kind != contract.JSON && v.Kind != contract.Text {
				return nil, ErrInvalid
			}
			var err error
			p.desired, err = completeJSON(v.Text)
			if err != nil {
				return nil, err
			}
			for name, v := range edit.Values {
				if name == "document" {
					continue
				}
				if name != "_member" && name != "_shard" {
					return nil, contract.ErrUnsupported
				}
				if p.kind == "insert" && (v.Null || v.Default) {
					continue
				}
				if v.Null || v.Default || v.Binary || len(v.Bytes) != 0 || v.Kind != "" && v.Kind != contract.Text {
					return nil, ErrInvalid
				}
				if p.kind == "update" {
					expected := p.identity.Unit.Member
					if name == "_shard" {
						expected = p.identity.Unit.Shard
					}
					if v.Text != expected {
						return nil, ErrInvalid
					}
				} else if name == "_member" {
					p.identity.Unit.Member = v.Text
				} else {
					p.identity.Unit.Shard = v.Text
				}
			}
		}
		plans = append(plans, p)
	}
	return plans, nil
}
func jsonEqual(a, b json.RawMessage) bool {
	x, e := canonical(a)
	y, f := canonical(b)
	return e == nil && f == nil && bytes.Equal(x, y)
}
func protectedSolrField(m *writeMember, key, name string) bool {
	return name == key || name == m.routeField || m.copies[name] || name == "_version_" || name == "_root_" || name == "_nest_path_" || name == "_childDocuments_" || name == "_route_"
}
func qualifyDesired(m *writeMember, key string, desired, original map[string]json.RawMessage, insert bool) (map[string]json.RawMessage, error) {
	delta := map[string]json.RawMessage{}
	for name, value := range desired {
		if protectedSolrField(m, key, name) {
			if insert {
				if name == key || name == m.routeField {
					continue
				}
				return nil, contract.ErrUnsupported
			}
			old, ok := original[name]
			if !ok || !jsonEqual(value, old) {
				return nil, contract.ErrUnsupported
			}
			continue
		}
		f, ok := m.field(name)
		if !ok || !*f.Stored {
			return nil, contract.ErrUnsupported
		}
		if !*f.MultiValued {
			var v any
			if decodeJSON(value, &v) != nil {
				return nil, ErrInvalid
			}
			if _, array := v.([]any); array {
				return nil, contract.ErrUnsupported
			}
		}
		if insert || !jsonEqual(value, original[name]) {
			delta[name] = bytes.Clone(value)
		}
	}
	if !insert {
		for name, value := range original {
			if _, ok := desired[name]; ok {
				continue
			}
			if protectedSolrField(m, key, name) {
				return nil, contract.ErrUnsupported
			}
			if _, ok := m.field(name); !ok {
				return nil, contract.ErrUnsupported
			}
			if !bytes.Equal(value, []byte("null")) {
				delta[name] = json.RawMessage("null")
			}
		}
	} else {
		for name, f := range m.fields {
			if *f.Required && name != "_version_" && len(f.Default) == 0 && len(desired[name]) == 0 {
				return nil, ErrInvalid
			}
		}
	}
	return delta, nil
}
func (l *dataLease) originalProjection(ctx context.Context, c *Client, p solrEditPlan) (unitProjection, error) {
	rule := contract.DataFilterRule{Column: "_key", Mode: "equal", Value: p.identity.Key.Text}
	page, err := c.selectProjection(ctx, l.object.Name, p.identity.Unit, &contract.FilterExpression[contract.DataFilterRule]{Op: "rule", Rule: &rule}, "", 2)
	if err != nil {
		return unitProjection{}, err
	}
	if len(page.Documents) != 1 || page.Total != 1 {
		return unitProjection{}, contract.ErrDataConflict
	}
	v := unitProjection{p.identity.Unit, page.Documents[0]}
	if !sameKey(v.Document.Key, p.identity.Key) || v.Document.Version != p.identity.Version || projectionFingerprint(v.Document.Document) != p.identity.Projection || !matchesSolrSnapshot(v, p.original) {
		return v, contract.ErrDataConflict
	}
	return v, nil
}
func nativeProjection(v *NativeDocument) (projection, error) {
	if v == nil || len(v.Projection) > 1<<20 {
		return projection{}, ErrBounds
	}
	return projection{Key: v.Ref.Key, Version: v.Version, Document: bytes.Clone(v.Projection)}, nil
}
func documentPreviews(v unitProjection) []contract.DataCell {
	out := []contract.DataCell{}
	for _, c := range projectionCells(v) {
		runes := []rune(c.Text)
		if len(runes) > 1024 {
			c.Text = string(runes[:1024])
			c.Truncated = true
		}
		copy := c
		out = append(out, contract.DataCell{Value: &copy, Text: c.Text, Truncated: c.Truncated})
	}
	return out
}

// Wait only for the original shard's search visibility. No commit, refresh
// command or mutation replay is issued; an absent auto-commit settles uncertain.
func (l *dataLease) indexedMutation(ctx context.Context, c *Client, p *solrEditPlan, version json.Number) (unitProjection, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	rule := contract.DataFilterRule{Column: "_key", Mode: "equal", Value: p.mutation.Ref.Key.Text}
	expression := &contract.FilterExpression[contract.DataFilterRule]{Op: "rule", Rule: &rule}
	for {
		page, err := c.selectProjection(ctx, l.object.Name, p.identity.Unit, expression, "", 2)
		if err != nil {
			return unitProjection{}, err
		}
		if p.kind == "delete" && page.Total == 0 && len(page.Documents) == 0 {
			return unitProjection{}, nil
		}
		if p.kind != "delete" && page.Total == 1 && len(page.Documents) == 1 && page.Documents[0].Version == version {
			if len(page.Documents[0].Document) > 1<<20 {
				return unitProjection{}, ErrBounds
			}
			return unitProjection{p.identity.Unit, page.Documents[0]}, nil
		}
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return unitProjection{}, ctx.Err()
		case <-timer.C:
		}
	}
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
	plans, err := l.prepareSolrEdits(db, edits)
	if err != nil {
		return nil, err
	}
	c, err := l.session.open(ctx)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	w, err := l.writeGuard(ctx, c)
	if err != nil {
		return nil, err
	}
	if !w.eligible {
		return nil, contract.ErrDataReadOnly
	}
	seen := map[string]bool{}
	// Resolve and validate the entire batch before dispatching its first write.
	for i := range plans {
		p := &plans[i]
		if p.kind == "insert" {
			if p.identity.Unit.Member == "" {
				if len(w.members) != 1 {
					return nil, ErrInvalid
				}
				for name := range w.members {
					p.identity.Unit.Member = name
				}
			}
			for _, u := range l.binding.Units {
				if u.Member == p.identity.Unit.Member {
					p.identity.Unit.Key = u.Key
					break
				}
			}
		} else if p.identity.Fingerprint != l.binding.Metadata.Fingerprint || !slices.Contains(l.binding.Units, p.identity.Unit) {
			return nil, ErrDataTargetChanged
		}
		m := w.members[p.identity.Unit.Member]
		if m == nil {
			return nil, ErrInvalid
		}
		var fields map[string]json.RawMessage
		if p.kind == "insert" {
			fields = p.desired
			p.identity.Key, err = keyCell(fields[p.identity.Unit.Key])
			if err != nil {
				return nil, err
			}
			if p.identity.Key.Kind != contract.Text || p.identity.Key.Text == "" {
				return nil, contract.ErrUnsupported
			}
			if m.router == "compositeId" && p.identity.Unit.Shard == "" {
				input := p.identity.Key.Text
				if m.routeField != "" {
					cell, e := keyCell(fields[m.routeField])
					if e != nil {
						return nil, e
					}
					input = cell.Text
				}
				hash, e := compositeHash(input)
				if e != nil {
					return nil, e
				}
				for shard, r := range m.ranges {
					if hash >= r.min && hash <= r.max {
						p.identity.Unit.Shard = shard
					}
				}
			}
			if m.router == "implicit" && p.identity.Unit.Shard == "" {
				if m.routeField != "" {
					cell, e := keyCell(fields[m.routeField])
					if e != nil {
						return nil, e
					}
					p.identity.Unit.Shard = cell.Text
				} else if len(m.ranges) == 1 {
					for shard := range m.ranges {
						p.identity.Unit.Shard = shard
					}
				} else {
					return nil, ErrInvalid
				}
			}
		} else {
			v, e := l.originalProjection(ctx, c, *p)
			if e != nil {
				return nil, e
			}
			fields, e = completeJSON(string(v.Document.Document))
			if e != nil {
				return nil, e
			}
		}
		ref, e := m.nativeRef(p.identity.Unit, p.identity.Key, fields)
		if e != nil {
			return nil, e
		}
		if e = c.validateDocumentRef(ref); e != nil {
			return nil, e
		}
		identity, _ := json.Marshal(struct {
			Unit readUnit
			Key  contract.Cell
		}{p.identity.Unit, p.identity.Key})
		if seen[string(identity)] {
			return nil, ErrInvalid
		}
		seen[string(identity)] = true
		kind := p.kind
		if kind == "insert" {
			kind = "create"
		}
		p.mutation = NativeDocumentMutation{Kind: kind, Ref: ref, Version: p.identity.Version}
		if p.kind == "insert" {
			p.mutation.Version = "-1"
		}
		if p.kind != "delete" {
			p.mutation.Fields, e = qualifyDesired(m, p.identity.Unit.Key, p.desired, fields, p.kind == "insert")
			if e != nil {
				return nil, e
			}
			if p.kind == "update" && len(p.mutation.Fields) == 0 {
				return nil, ErrInvalid
			}
		}
		if _, e = c.nativeMutationRequest(p.mutation); e != nil {
			return nil, e
		}
	}
	result := &contract.DataEditResult{}
	batch := &pendingSolrBatch{plans: plans, result: result, binding: w.signature}
	defer func() {
		if !result.CommitConfirmed && (result.CommitUncertain || result.Applied > 0) {
			result.Incomplete = result.Incomplete || batch.attempted < len(plans)
			batch.result = cloneSolrResult(result)
			batch.receipt = l.sign(result)
			l.pending = batch
		}
	}()
	for i := range plans {
		p := &plans[i]
		if _, err = l.writeGuard(ctx, c); err != nil {
			return result, err
		}
		before, e := c.ReadNativeDocument(ctx, p.mutation.Ref)
		if p.kind == "insert" {
			if e == nil {
				return result, contract.ErrDataConflict
			}
			if !errors.Is(e, ErrDocumentNotFound) {
				return result, e
			}
		} else {
			if e != nil {
				return result, e
			}
			if before.Version != p.identity.Version {
				return result, contract.ErrDataConflict
			}
		}
		if _, err = l.writeGuard(ctx, c); err != nil {
			return result, err
		}
		outcome := contract.DataEditOutcome{Kind: p.kind, Original: cloneSolrSnapshot(p.original), PreviewColumns: dataFields()}
		batch.attempted = i + 1
		result.Outcomes = append(result.Outcomes, outcome)
		receipt, e := c.mutateNativeDocument(ctx, p.mutation, w.members[p.identity.Unit.Member].chain)
		p.receipt = receipt
		// Once dispatch starts, errors are observations, never permission to replay.
		if e != nil {
			result.CommitUncertain = true
			result.RefreshRequired = true
			return result, errors.Join(contract.ErrDataCommitUncertain, e)
		}
		result.Applied++
		result.RefreshRequired = true
		if _, e = l.writeGuard(ctx, c); e != nil {
			result.CommitUncertain = true
			return result, errors.Join(contract.ErrDataCommitUncertain, e)
		}
		saved, e := c.ReadNativeDocument(ctx, p.mutation.Ref)
		if p.kind == "delete" {
			if !errors.Is(e, ErrDocumentNotFound) {
				result.CommitUncertain = true
				return result, errors.Join(contract.ErrDataCommitUncertain, e)
			}
			if _, e = l.indexedMutation(ctx, c, p, ""); e != nil {
				result.CommitUncertain = true
				return result, errors.Join(contract.ErrDataCommitUncertain, e)
			}
		} else {
			if e != nil || !matchesMutation(p, saved) {
				result.CommitUncertain = true
				return result, errors.Join(contract.ErrDataCommitUncertain, e)
			}
			v, e := l.indexedMutation(ctx, c, p, saved.Version)
			if e != nil {
				result.CommitUncertain = true
				return result, errors.Join(contract.ErrDataCommitUncertain, e)
			}
			outcome.Saved = l.snapshot(db, v)
			outcome.Previews = documentPreviews(v)
		}
		if _, e = l.writeGuard(ctx, c); e != nil {
			result.CommitUncertain = true
			return result, errors.Join(contract.ErrDataCommitUncertain, e)
		}
		result.Outcomes[i] = outcome
	}
	result.CommitConfirmed = true
	return result, nil
}
func matchesMutation(p *solrEditPlan, d *NativeDocument) bool {
	if d == nil || p.kind == "delete" {
		return false
	}
	if p.receipt != nil && p.receipt.Version != "" {
		return d.Version == p.receipt.Version
	}
	if p.kind == "update" && d.Version == p.identity.Version {
		return false
	}
	fields, err := completeJSON(string(d.Projection))
	if err != nil {
		return false
	}
	for name, want := range p.mutation.Fields {
		got, ok := fields[name]
		if !ok && bytes.Equal(bytes.TrimSpace(want), []byte("null")) {
			continue
		}
		if !jsonEqual(got, want) {
			return false
		}
	}
	return true
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
	recovered := cloneSolrResult(batch.result)
	all := true
	for i := range batch.plans {
		p := &batch.plans[i]
		state := "indeterminate"
		if i >= batch.attempted {
			verification.States = append(verification.States, "not_processed")
			all = false
			continue
		}
		w, e := l.writeGuard(ctx, c)
		if errors.Is(e, ErrDataTargetChanged) {
			verification.States = append(verification.States, state)
			all = false
			continue
		}
		if e != nil {
			return nil, e
		}
		if w.signature != batch.binding {
			return nil, ErrDataTargetChanged
		}
		doc, e := c.ReadNativeDocument(ctx, p.mutation.Ref)
		if e != nil && !errors.Is(e, ErrDocumentNotFound) {
			return nil, e
		}
		// Absence proves the desired delete state is currently observed, not its
		// historical attribution or durability. A confirmed receipt is required to
		// settle deletes. Unknown creates/deletes stay indeterminate, without replay.
		if p.kind == "delete" && errors.Is(e, ErrDocumentNotFound) && p.receipt != nil && p.receipt.State == "acknowledged" {
			if _, e = l.indexedMutation(ctx, c, p, ""); e == nil {
				state = "applied"
			}
		} else if e == nil {
			if p.kind != "insert" && doc.Version == p.identity.Version {
				state = "not_applied"
			} else if p.kind != "delete" && matchesMutation(p, doc) {
				state = "applied"
				u, e := l.indexedMutation(ctx, c, p, doc.Version)
				if e != nil {
					return nil, e
				}
				recovered.Outcomes[i].Saved = l.snapshot(db, u)
				recovered.Outcomes[i].Previews = documentPreviews(u)
			}
		}
		if _, e = l.writeGuard(ctx, c); errors.Is(e, ErrDataTargetChanged) {
			state = "indeterminate"
		} else if e != nil {
			return nil, e
		}
		verification.States = append(verification.States, state)
		all = all && state == "applied"
	}
	if all && batch.attempted == len(batch.plans) && len(recovered.Outcomes) == len(batch.plans) {
		recovered.CommitConfirmed = true
		recovered.CommitUncertain = false
		recovered.Incomplete = false
		recovered.Verified = true
		recovered.Applied = len(batch.plans)
		verification.Status = "applied"
		verification.Recovered = recovered
		l.pending = nil
	}
	return verification, nil
}
