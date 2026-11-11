package contract

import (
	"context"
	"slices"
)

type KVSnapshotFileKind string

const (
	KVSnapshotBackendFile KVSnapshotFileKind = "backend"
	KVSnapshotJournalFile KVSnapshotFileKind = "native-journal"
)

// Replace authorizes displacement of the inspected existing file only.
type KVSnapshotFileChoice struct {
	Path    string
	Kind    KVSnapshotFileKind
	Replace bool
}

type KVSnapshotFileObservation struct {
	Source   KVSnapshotCapture
	Choice   KVSnapshotFileChoice
	Existing *SnapshotSource
}

func (r KVSnapshotFileObservation) Clone() KVSnapshotFileObservation {
	r.Source = r.Source.Clone()
	if r.Existing != nil {
		e := *r.Existing
		r.Existing = &e
	}
	return r
}

type KVSnapshotFilePreparation interface {
	Observation() KVSnapshotFileObservation
	Close() error
}

// Publication acknowledgement, later observations and fsync are independent.
// Displaced user files are preserved, including after successful replacement.
// A failed acknowledgement stays unknown even if DestinationMatched is true.
// Paths name the originally captured directory. PathMatched separately records
// whether that directory still occupies the selected parent pathname.
type KVSnapshotFileResult struct {
	Original                                              KVSnapshotFileObservation
	Attempted, Ended, Canceled                            bool
	SourceVerified, FileSynced                            bool
	DisplacementAttempted, Displaced, DisplacementUnknown bool
	RestorationAttempted, Restored, RestorationUnknown    bool
	PublicationAttempted, Published, PublicationUnknown   bool
	DestinationMatched, PathMatched, DirectorySynced      bool
	TemporaryRetained, DisplacedRetained                  bool
	AuxiliaryPathMatched                                  bool
	BytesWritten                                          uint64
	SHA256                                                []byte
	TemporaryPath, DisplacedPath, DestinationPath         string
	Error, CleanupError                                   string
}

func (r KVSnapshotFileResult) Clone() KVSnapshotFileResult {
	r.Original = r.Original.Clone()
	r.SHA256 = slices.Clone(r.SHA256)
	return r
}

type KVSnapshotFiles interface {
	PrepareFile(context.Context, KVSnapshotArtifact, KVSnapshotFileChoice) (KVSnapshotFilePreparation, error)
	PublishFile(context.Context, KVSnapshotFilePreparation) (KVSnapshotFileResult, error)
}
