package kvworkspace

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

func TestAuthPreviewFileCapturesOriginalRedactedStepsAndCancelNeverWrites(t *testing.T) {
	for _, cancel := range []bool{false, true} {
		s, d, _ := authFormState(t)
		s.OpenAuthAction(d, "UserAdd")
		d.authDraft.name, d.authDraft.password, d.authDraft.initialRoles = "original user", "original secret", "original role"
		s.PrepareAuthAction(d)
		before := d.Request()
		dirty := d.Dirty()
		entered, release := make(chan struct{}), make(chan struct{})
		var written []byte
		s.deps.Files = Files{SavePath: func(string) (string, error) {
			close(entered)
			<-release
			if cancel {
				return "", nil
			}
			return "original.json", nil
		}, WriteStream: func(_ string, write func(io.Writer) error) error {
			var b bytes.Buffer
			err := write(&b)
			written = bytes.Clone(b.Bytes())
			return err
		}}
		s.ExportAuthPreview(d)
		finished := d.io.finished
		await(t, entered)
		s.CancelConfirmation(d)
		s.OpenAuthAction(d, "RoleAdd")
		d.authDraft.name = "replacement role"
		close(release)
		await(t, finished)
		s.Consume()
		if cancel {
			if len(written) != 0 {
				t.Fatal("canceled picker wrote an Auth record")
			}
		} else {
			if strings.Contains(string(written), "original secret") || !strings.Contains(string(written), "original role") || strings.Contains(string(written), "replacement role") || !strings.Contains(string(written), "request_omitted_fields") {
				t.Fatal("export followed replacement form or leaked original password")
			}
			if _, err := DecodeRequest(written); err == nil {
				t.Fatal("redacted preview accepted for execution")
			}
		}
		if d.Request() != before || d.path != "" || d.Dirty() != dirty {
			t.Fatal("preview export changed original document baseline")
		}
	}
}
