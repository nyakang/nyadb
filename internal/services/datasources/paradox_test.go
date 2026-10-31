//go:build linux || darwin || freebsd || netbsd || windows

package datasources

import (
	"testing"

	"github.com/ebitengine/purego"
)

func TestParadoxErrorCallbackCanBeCalledWithNativeSignature(t *testing.T) {
	callback := paradoxErrorHandler()
	if callback == 0 || callback != paradoxErrorHandler() {
		t.Fatal("Paradox readers must reuse a valid native callback")
	}
	// Exercise registration and invocation without requiring libpx. Its error
	// handler returns void, even though the Windows Go adapter must return a word.
	var report func(uintptr, int32, uintptr, uintptr)
	purego.RegisterFunc(&report, callback)
	report(0, -1, 0, 0)
}
