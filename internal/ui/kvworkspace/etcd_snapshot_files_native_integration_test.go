package kvworkspace

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/egoist/mygo/i18n"
	"github.com/nyakang/nyadb/internal/db/contract"
)

// Faults at the actual caller acknowledgement boundary preserve the original
// complete file I/O. The captured native executor remains the original store.
type snapshotFileBoundary struct {
	base                contract.KVSnapshotFiles
	mode, parent, moved string
	calls               atomic.Int32
}

func (f *snapshotFileBoundary) PrepareFile(ctx context.Context, a contract.KVSnapshotArtifact, choice contract.KVSnapshotFileChoice) (contract.KVSnapshotFilePreparation, error) {
	return f.base.PrepareFile(ctx, a, choice)
}
func (f *snapshotFileBoundary) PublishFile(ctx context.Context, plan contract.KVSnapshotFilePreparation) (contract.KVSnapshotFileResult, error) {
	f.calls.Add(1)
	observation := plan.Observation()
	if f.mode == "appeared" {
		file, err := os.OpenFile(observation.Choice.Path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return contract.KVSnapshotFileResult{Original: observation, Ended: true}, err
		}
		_, err = file.Write([]byte("actual independently appeared target"))
		if err = errors.Join(err, file.Close()); err != nil {
			return contract.KVSnapshotFileResult{Original: observation, Ended: true}, err
		}
	}
	if f.mode == "moved" {
		if err := os.Rename(f.parent, f.moved); err != nil {
			return contract.KVSnapshotFileResult{Original: observation, Ended: true}, err
		}
		if err := os.Mkdir(f.parent, 0700); err != nil {
			return contract.KVSnapshotFileResult{Original: observation, Ended: true}, err
		}
		if err := os.WriteFile(observation.Choice.Path, []byte("actual foreign parent target"), 0600); err != nil {
			return contract.KVSnapshotFileResult{Original: observation, Ended: true}, err
		}
	}
	r, err := f.base.PublishFile(ctx, plan)
	if f.mode == "lost" && err == nil && r.Published {
		r.Published, r.PublicationUnknown = false, true
		r.Error = "original caller acknowledgement withheld after actual file publication"
		return r, errors.New(r.Error)
	}
	return r, err
}
func snapshotBilingualCapture(t *testing.T, s *State, d *Document, stage string, scroll bool) {
	t.Helper()
	if os.Getenv("NYADB_ETCD_WORKSPACE_IMAGES") == "" {
		return
	}
	previous := i18n.Locale()
	defer i18n.SetLocaleFunc(func() string { return previous })
	for _, language := range []string{"zh-Hans", "en"} {
		i18n.SetLocaleFunc(func() string { return language })
		for _, size := range [][2]int{{1280, 800}, {720, 520}} {
			tt := workspaceTester(s, d, size[0], size[1])
			tt.Frame()
			if scroll {
				tt.Scroll(float32(size[0]-30), float32(size[1]-60), 0, 1000)
				tt.Frame()
			}
			workspaceCapture(t, tt, language, size, stage)
		}
	}
}
func TestEtcdNativeSnapshotWorkspaceActualPublicationLossAppearedTargetAndOriginalParent(t *testing.T) {
	requireSnapshotWorkspace(t)
	p, secret := workspaceProfile(t)
	for _, mode := range []string{"lost", "appeared", "moved"} {
		t.Run(mode, func(t *testing.T) {
			proxy, source := snapshotWorkspaceProxy(t, p, "complete", nil)
			s, d, _ := workspaceNativeState(t, proxy, secret)
			parent := filepath.Join(t.TempDir(), "selected-parent")
			if err := os.Mkdir(parent, 0700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(parent, "original.db")
			store := snapshotNativeStore(t, s, path)
			workspaceInput(s, d, DefaultSnapshotRequest())
			snapshotNativeCapture(t, s, d)
			backend, _ := snapshotNativeBytes(t, d)
			original, _ := d.SnapshotReceipt()
			boundary := &snapshotFileBoundary{base: store, mode: mode, parent: parent, moved: parent + "-original"}
			s.SetSnapshotOperations(store, SnapshotFiles{Operations: boundary, SavePath: func(contract.KVSnapshotFileKind, string) (string, error) { return path, nil }})
			old := []byte("original old backup before directory replacement")
			if mode == "moved" {
				if err := os.WriteFile(path, old, 0600); err != nil {
					t.Fatal(err)
				}
			}
			snapshotNativeChooseFile(t, s, d, contract.KVSnapshotBackendFile)
			snapshotNativePublish(t, s, d)
			files := d.SnapshotFileReceipts()
			if len(files) != 1 || !reflect.DeepEqual(files[0].Original.Source, original) || source.calls.Load() != 1 {
				t.Fatal("publication changed source ownership", d.Error())
			}
			r := files[0]
			d.snapshot.recordView = 2
			snapshotBilingualCapture(t, s, d, "snapshot-file-"+mode, false)
			snapshotBilingualCapture(t, s, d, "snapshot-file-"+mode+"-details", true)
			switch mode {
			case "lost":
				snapshotNativeMatch(t, path, backend)
				if r.Published || !r.PublicationUnknown || !r.DestinationMatched || !r.PathMatched {
					t.Fatal("later match promoted lost caller ACK", r)
				}
				boundary.mode = "complete"
				snapshotNativeChooseFile(t, s, d, contract.KVSnapshotBackendFile)
				if d.snapshot.fileConfirmation.checked || d.snapshot.fileConfirmation.observation.Existing == nil {
					t.Fatal("later replacement reused earlier file authority")
				}
				snapshotNativePublish(t, s, d)
				later := d.SnapshotFileReceipts()
				if len(later) != 2 || !later[0].PublicationUnknown || later[0].Published || !later[1].Published {
					t.Fatal("later save rewrote original unknown", later)
				}
				snapshotNativeMatch(t, later[1].DisplacedPath, backend)
			case "appeared":
				snapshotNativeMatch(t, path, []byte("actual independently appeared target"))
				if r.Published || r.Displaced {
					t.Fatal("appeared user file overwritten", r)
				}
				boundary.mode = "complete"
				snapshotNativeChooseFile(t, s, d, contract.KVSnapshotBackendFile)
				if d.snapshot.fileConfirmation.checked || d.snapshot.fileConfirmation.observation.Existing == nil {
					t.Fatal("appeared file lacked explicit original replacement confirmation")
				}
				snapshotNativePublish(t, s, d)
				later := d.SnapshotFileReceipts()[1]
				snapshotNativeMatch(t, path, backend)
				snapshotNativeMatch(t, later.DisplacedPath, []byte("actual independently appeared target"))
			case "moved":
				snapshotNativeMatch(t, path, []byte("actual foreign parent target"))
				snapshotNativeMatch(t, filepath.Join(boundary.moved, "original.db"), backend)
				if !r.Published || r.PathMatched || r.AuxiliaryPathMatched || !r.DisplacedRetained {
					t.Fatal("moved original parent relabeled as current", r)
				}
				actualBackup := filepath.Join(boundary.moved, filepath.Base(filepath.Dir(r.DisplacedPath)), filepath.Base(r.DisplacedPath))
				snapshotNativeMatch(t, actualBackup, old)
			}
			if boundary.calls.Load() != map[string]int32{"lost": 2, "appeared": 2, "moved": 1}[mode] || source.calls.Load() != 1 {
				t.Fatal("file operation or native Snapshot replayed")
			}
			s.Close(d)
			if mode == "moved" {
				snapshotNativeMatch(t, path, []byte("actual foreign parent target"))
			} else {
				snapshotNativeMatch(t, path, backend)
			}
		})
	}
}
func TestEtcdNativeSnapshotWorkspacePreservesOriginalNonUTF8FilePathBytes(t *testing.T) {
	requireSnapshotWorkspace(t)
	p, secret := workspaceProfile(t)
	s, d, _ := workspaceNativeState(t, p, secret)
	path := filepath.Join(t.TempDir(), "original-\xff.db")
	snapshotNativeStore(t, s, path)
	workspaceInput(s, d, DefaultSnapshotRequest())
	snapshotNativeCapture(t, s, d)
	backend, _ := snapshotNativeBytes(t, d)
	snapshotNativeChooseFile(t, s, d, contract.KVSnapshotBackendFile)
	snapshotNativePublish(t, s, d)
	snapshotNativeMatch(t, path, backend)
	raw, err := s.SnapshotRecord(d)
	var record struct {
		Files []struct{ ChoicePathBytes, DestinationPathBytes []byte } `json:"original_file_publications"`
	}
	if err != nil || json.Unmarshal(raw, &record) != nil || len(record.Files) != 1 || !bytes.Equal(record.Files[0].ChoicePathBytes, []byte(path)) || !bytes.Equal(record.Files[0].DestinationPathBytes, []byte(path)) {
		t.Fatal("JSON archive lost original native path bytes", err)
	}
}
