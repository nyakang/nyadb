package kvworkspace

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"strconv"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/jsondocument"
)

const snapshotArchiveLimit = 512 << 20
const snapshotNativeRecordReserve = 300 << 20
const snapshotFileRecordReserve = 4 << 20
const snapshotFileRecordCount = 128

type snapshotLimitsRecord struct {
	BodyBytes    string `json:"body_bytes"`
	JournalBytes string `json:"journal_bytes"`
	Frames       string `json:"frames"`
}

type snapshotNativeRecord struct {
	Document                                                                        string               `json:"original_document_id"`
	Scope                                                                           leaseScopeRecord     `json:"original_scope"`
	Request                                                                         snapshotLimitsRecord `json:"original_limits"`
	Attempted, Ended, Completed                                                     bool
	NativeTerminalObserved, EOF, SourceComplete, ChecksumMatched                    bool
	JournalComplete, OutputAcknowledged, Canceled                                   bool
	Frames, ExpectedBodyBytes, BodyBytes, SnapshotBytesWritten, JournalBytesWritten string
	StorageVersion                                                                  string
	BodySHA256, Checksum, JournalSHA256                                             string
	NativeStatus, RejectedNative                                                    []byte
	NativeCode, LocalError, CaptureError                                            string
	Before                                                                          json.RawMessage `json:"original_before_receipt"`
}

func snapshotNativeData(r contract.KVSnapshotResult) (snapshotNativeRecord, error) {
	before, err := encodeOperationReceipt(Receipt{Scope: r.Scope.Original.Clone(), Authored: r.Before.Request, Result: r.Before.Clone()})
	return snapshotNativeRecord{Document: strconv.FormatUint(r.Scope.DocumentID, 10), Scope: leaseScope(r.Scope.Original), Request: snapshotLimitsRecord{strconv.FormatUint(r.Request.BodyBytes, 10), strconv.FormatUint(r.Request.JournalBytes, 10), strconv.FormatUint(r.Request.Frames, 10)}, Attempted: r.Attempted, Ended: r.Ended, Completed: r.Completed, NativeTerminalObserved: r.NativeTerminalObserved, EOF: r.EOF, SourceComplete: r.SourceComplete, ChecksumMatched: r.ChecksumMatched, JournalComplete: r.JournalComplete, OutputAcknowledged: r.OutputAcknowledged, Canceled: r.Canceled, Frames: strconv.FormatUint(r.Frames, 10), ExpectedBodyBytes: strconv.FormatUint(r.ExpectedBodyBytes, 10), BodyBytes: strconv.FormatUint(r.BodyBytes, 10), SnapshotBytesWritten: strconv.FormatUint(r.SnapshotBytesWritten, 10), JournalBytesWritten: strconv.FormatUint(r.JournalBytesWritten, 10), StorageVersion: r.StorageVersion, BodySHA256: hex.EncodeToString(r.BodySHA256), Checksum: hex.EncodeToString(r.Checksum), JournalSHA256: hex.EncodeToString(r.JournalSHA256), NativeStatus: r.NativeStatus, RejectedNative: r.RejectedNative, NativeCode: r.NativeCode, LocalError: r.LocalError, CaptureError: r.CaptureError, Before: before}, err
}

type snapshotCaptureRecord struct {
	Native                        snapshotNativeRecord `json:"original_native_source"`
	SnapshotBytes, JournalBytes   string
	SnapshotSHA256, JournalSHA256 string
	Stored, FilesSynced, Complete bool
	StorageError, CleanupError    string
}

func snapshotCaptureData(r contract.KVSnapshotCapture) (snapshotCaptureRecord, error) {
	n, err := snapshotNativeData(r.Original)
	return snapshotCaptureRecord{n, strconv.FormatUint(r.SnapshotBytes, 10), strconv.FormatUint(r.JournalBytes, 10), hex.EncodeToString(r.SnapshotSHA256), hex.EncodeToString(r.JournalSHA256), r.Stored, r.FilesSynced, r.Complete, r.StorageError, r.CleanupError}, err
}

type snapshotExistingRecord struct {
	Name, Size, SHA256 string
	NameBytes          []byte
}

// Each file record references the one complete original source rather than
// duplicating a possibly large original native receipt in every save record.
type snapshotFileRecord struct {
	SourceDocument                                               string
	SourceScope                                                  leaseScopeRecord
	SourceSnapshotSHA256, SourceJournalSHA256                    string
	Choice                                                       contract.KVSnapshotFileChoice
	ChoicePathBytes                                              []byte
	Existing                                                     *snapshotExistingRecord
	Attempted, Ended, Canceled                                   bool
	CancelRequested                                              bool
	SourceVerified, FileSynced                                   bool
	DisplacementAttempted, Displaced, DisplacementUnknown        bool
	RestorationAttempted, Restored, RestorationUnknown           bool
	PublicationAttempted, Published, PublicationUnknown          bool
	DestinationMatched, PathMatched, DirectorySynced             bool
	TemporaryRetained, DisplacedRetained, AuxiliaryPathMatched   bool
	BytesWritten, SHA256                                         string
	TemporaryPath, DisplacedPath, DestinationPath                string
	TemporaryPathBytes, DisplacedPathBytes, DestinationPathBytes []byte
	Error, CleanupError                                          string
}

func snapshotFileData(r contract.KVSnapshotFileResult) snapshotFileRecord {
	v := snapshotFileRecord{SourceDocument: strconv.FormatUint(r.Original.Source.Original.Scope.DocumentID, 10), SourceScope: leaseScope(r.Original.Source.Original.Scope.Original), SourceSnapshotSHA256: hex.EncodeToString(r.Original.Source.SnapshotSHA256), SourceJournalSHA256: hex.EncodeToString(r.Original.Source.JournalSHA256), Choice: r.Original.Choice, Attempted: r.Attempted, Ended: r.Ended, Canceled: r.Canceled, SourceVerified: r.SourceVerified, FileSynced: r.FileSynced, DisplacementAttempted: r.DisplacementAttempted, Displaced: r.Displaced, DisplacementUnknown: r.DisplacementUnknown, RestorationAttempted: r.RestorationAttempted, Restored: r.Restored, RestorationUnknown: r.RestorationUnknown, PublicationAttempted: r.PublicationAttempted, Published: r.Published, PublicationUnknown: r.PublicationUnknown, DestinationMatched: r.DestinationMatched, PathMatched: r.PathMatched, DirectorySynced: r.DirectorySynced, TemporaryRetained: r.TemporaryRetained, DisplacedRetained: r.DisplacedRetained, AuxiliaryPathMatched: r.AuxiliaryPathMatched, BytesWritten: strconv.FormatUint(r.BytesWritten, 10), SHA256: hex.EncodeToString(r.SHA256), TemporaryPath: r.TemporaryPath, DisplacedPath: r.DisplacedPath, DestinationPath: r.DestinationPath, Error: r.Error, CleanupError: r.CleanupError}
	if old := r.Original.Existing; old != nil {
		v.Existing = &snapshotExistingRecord{old.Name, strconv.FormatUint(old.Size, 10), old.SHA256, []byte(old.Name)}
	}
	// Unix paths can contain bytes that JSON strings cannot represent exactly.
	// Keep original path bytes beside readable strings in the complete archive.
	v.ChoicePathBytes = []byte(r.Original.Choice.Path)
	v.TemporaryPathBytes, v.DisplacedPathBytes, v.DestinationPathBytes = []byte(r.TemporaryPath), []byte(r.DisplacedPath), []byte(r.DestinationPath)
	return v
}
func encodeSnapshotRecord(x *snapshotState) ([]byte, error) {
	v := struct {
		Format          string                 `json:"format"`
		Capture         *snapshotCaptureRecord `json:"original_capture"`
		Authored        *contract.KVRequest    `json:"original_authored_request"`
		CancelRequested bool                   `json:"capture_cancel_requested"`
		CleanupError    string                 `json:"artifact_close_error"`
		Files           []snapshotFileRecord   `json:"original_file_publications"`
		Recovered       []snapshotNativeRecord `json:"original_recovered_sources"`
		Semantics       string                 `json:"semantics"`
	}{Format: "nyadb-original-etcd-snapshot-v1", Semantics: "The original complete source, private artifact, file fsync, no-replace displacement/restoration/publication acknowledgements, later destination/path observations, directory fsync and cleanup are independent. Whole backend scope ignores browsing prefix. Complete original failure journals can save without completing the backend. Published/displaced user files are never deleted as rollback. Failed acknowledgement remains unknown after a matching observation. Recorded auxiliary paths select the original captured directory and are current only when PathMatched and AuxiliaryPathMatched. Recovery performs no Snapshot, file publication or restore and settles no earlier unknown KV/member/Alarm write. Inspection and request/receipt files grant no authority."}
	if x != nil {
		if x.original != nil {
			capture, err := snapshotCaptureData(*x.original)
			if err != nil {
				return nil, err
			}
			v.Capture = &capture
		}
		v.CancelRequested = x.captureCanceled
		v.Authored = x.authored
		v.CleanupError = x.cleanupError
		for i, r := range x.files {
			file := snapshotFileData(r)
			if i < len(x.fileCancelRequested) {
				file.CancelRequested = x.fileCancelRequested[i]
			}
			v.Files = append(v.Files, file)
		}
		for _, r := range x.recovered {
			n, err := snapshotNativeData(r)
			if err != nil {
				return nil, err
			}
			v.Recovered = append(v.Recovered, n)
		}
	}
	raw, err := json.MarshalIndent(v, "", "  ")
	if len(raw) > snapshotArchiveLimit {
		return nil, jsondocument.ErrBounds
	}
	return raw, err
}
func snapshotRecordRoom(x *snapshotState, reserve int) bool {
	if reserve < 0 || reserve > snapshotArchiveLimit {
		return false
	}
	raw, err := encodeSnapshotRecord(x)
	return err == nil && len(raw) <= snapshotArchiveLimit-reserve
}
func (s *State) SnapshotRecord(d *Document) ([]byte, error) {
	if !s.owned(d) {
		return nil, jsondocument.ErrInvalid
	}
	return encodeSnapshotRecord(d.snapshot)
}
func (s *State) ExportSnapshotRecord(d *Document) {
	if !s.active(d) || d.FileBusy() || !d.snapshotContent() || s.deps.Files.SavePath == nil || s.deps.Files.WriteStream == nil {
		return
	}
	raw, err := s.SnapshotRecord(d)
	if err != nil {
		d.error = err.Error()
		return
	}
	files := s.deps.Files
	s.fileJob(d, "snapshot-record-export", func(ctx context.Context) fileResult {
		path, err := files.SavePath("etcd-snapshot.nyakv-receipt.json")
		if err != nil || path == "" {
			return fileResult{err: err}
		}
		return fileResult{path: path, err: writeCaptured(ctx, files, path, raw)}
	})
}
func (s *State) validateSnapshotConfirmation(d *Document) {
	if d.snapshot == nil {
		return
	}
	p := d.snapshot.confirmation
	if p != nil && (p.lease != d.lease || p.authored != d.Request() || d.needsRecovery || !sameEventScope(p.scope.Original, d.scope)) {
		d.snapshot.confirmation = nil
	}
}
func (s *State) consumeSnapshotRecovery(d *Document, job *operation, r *contract.KVSnapshotResult) {
	if r == nil || job.snapshotRecovery == nil || !reflect.DeepEqual(*r, *job.snapshotRecovery) || r.Scope.DocumentID != d.id {
		return
	}
	x := d.snapshotState()
	if x.original != nil && reflect.DeepEqual(x.original.Original, *r) {
		return
	}
	for _, previous := range x.recovered {
		if reflect.DeepEqual(previous, *r) {
			return
		}
	}
	x.recovered = append(x.recovered, r.Clone())
	x.revision++
	d.permit = nil
}

func snapshotRecoveryExpected(d *Document) *contract.KVSnapshotResult {
	if facet, ok := d.lease.(contract.KVSnapshots); ok {
		r := facet.LastSnapshotResult().Clone()
		if r.Scope.DocumentID == d.id && r.Scope.Original.Namespace.ProfileID == d.profileID && r.Scope.Original.Generation > 0 {
			return &r
		}
	}
	if d.snapshot != nil && d.snapshot.original != nil && d.snapshot.original.Original.Ended {
		r := d.snapshot.original.Original.Clone()
		return &r
	}
	return nil
}
func snapshotRecoveryRoom(x *snapshotState, expected *contract.KVSnapshotResult) bool {
	if expected == nil {
		return true
	}
	if x != nil {
		if x.original != nil && reflect.DeepEqual(x.original.Original, *expected) {
			return true
		}
		for _, r := range x.recovered {
			if reflect.DeepEqual(r, *expected) {
				return true
			}
		}
	}
	n, err := snapshotNativeData(*expected)
	if err != nil {
		return false
	}
	raw, err := json.MarshalIndent(n, "", "  ")
	return err == nil && len(raw) < snapshotArchiveLimit && snapshotRecordRoom(x, len(raw)+4096)
}
func sameSnapshotRecovery(expected, actual *contract.KVSnapshotResult) bool {
	return actual == nil || expected != nil && reflect.DeepEqual(*actual, *expected)
}
