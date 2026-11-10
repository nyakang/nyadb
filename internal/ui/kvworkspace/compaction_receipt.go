package kvworkspace

import (
	"context"
	"encoding/json"
	"strconv"
)

type compactionObservationRecord struct {
	Document string            `json:"original_document_id"`
	Scope    leaseScopeRecord  `json:"original_scope"`
	Authored string            `json:"authored_compact_json"`
	Revision string            `json:"requested_revision"`
	Physical bool              `json:"physical"`
	Requests []requestRecord   `json:"planned_read_requests"`
	Steps    []json.RawMessage `json:"original_read_receipts"`
	Captured bool              `json:"read_sequence_captured"`
}

type requestRecord struct {
	Method string `json:"method"`
	JSON   string `json:"json"`
}
type compactionRecord struct {
	Document    string                       `json:"original_document_id"`
	Scope       leaseScopeRecord             `json:"original_scope"`
	Revision    string                       `json:"requested_revision"`
	Physical    bool                         `json:"physical"`
	Observation *compactionObservationRecord `json:"earlier_observation"`
	Semantics   string                       `json:"semantics"`
}

func compactionObservationJSON(r CompactionObservation) ([]byte, error) {
	v, err := compactionObservationData(r)
	if err != nil {
		return nil, err
	}
	return json.MarshalIndent(v, "", "  ")
}

func compactionObservationData(r CompactionObservation) (*compactionObservationRecord, error) {
	v := &compactionObservationRecord{Document: strconv.FormatUint(r.DocumentID, 10), Scope: leaseScope(r.Scope), Authored: r.Authored.JSON, Revision: strconv.FormatInt(r.Revision, 10), Physical: r.Physical, Captured: r.Captured}
	for _, request := range r.Requests {
		v.Requests = append(v.Requests, requestRecord{request.Method, request.JSON})
	}
	for _, step := range r.Steps {
		raw, err := encodeOperationReceipt(step)
		if err != nil {
			return nil, err
		}
		v.Steps = append(v.Steps, json.RawMessage(raw))
	}
	return v, nil
}

func compactionData(r *CompactionReceipt) (*compactionRecord, error) {
	if r == nil {
		return nil, nil
	}
	observation, err := compactionObservationData(r.Observation)
	if err != nil {
		return nil, err
	}
	return &compactionRecord{Document: strconv.FormatUint(r.DocumentID, 10), Scope: leaseScope(r.Scope), Revision: strconv.FormatInt(r.Revision, 10), Physical: r.Physical, Observation: observation, Semantics: "Native KV/Compact affects cluster history regardless of the browse prefix. Earlier Status/Range receipts are observations, not CAS protection or authorization. Current values remain. Physical waits for backend compaction; this is not Defragment. Later reads do not settle a missing original Compact acknowledgement."}, nil
}

func compactionPreviewJSON(r *CompactionReceipt) ([]byte, error) {
	v, err := compactionData(r)
	if err != nil {
		return nil, err
	}
	return json.MarshalIndent(v, "", "  ")
}

func (s *State) ExportCompactionObservation(d *Document) {
	if !s.active(d) || d.FileBusy() || d.compactionObservation == nil || s.deps.Files.SavePath == nil || s.deps.Files.WriteStream == nil {
		return
	}
	raw, err := compactionObservationJSON(d.compactionObservation.Clone())
	if err != nil {
		d.error = err.Error()
		return
	}
	f := s.deps.Files
	s.fileJob(d, "export", func(ctx context.Context) fileResult {
		path, err := f.SavePath("Compact.nyakv-observation.json")
		if err != nil || path == "" {
			return fileResult{err: err}
		}
		return fileResult{path: path, err: writeCaptured(ctx, f, path, raw)}
	})
}
