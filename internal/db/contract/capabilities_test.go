package contract

import (
	"context"
	"testing"
)

func TestDataCapabilitiesRequireEveryEditToHaveAnExecutor(t *testing.T) {
	preview := func(context.Context, int64, string, string, []DataEdit) (DataMutationPlan, error) { return nil, nil }
	submit := func(context.Context, int64, string, string, DataMutationPlan) (*DataEditResult, error) {
		return nil, nil
	}
	copyPreview := func(context.Context, int64, string, string, DataKeyCopy) (DataMutationPlan, error) { return nil, nil }
	for _, tc := range []struct {
		name    string
		runtime DataRuntime
		want    bool
	}{
		{"complete", DataRuntime{PreviewEdits: preview, SubmitEdits: submit, PreviewEditKinds: []string{"insert", "update", "delete"}}, true},
		{"partial", DataRuntime{PreviewEdits: preview, SubmitEdits: submit, PreviewEditKinds: []string{"insert", "delete"}}, false},
		{"only-update", DataRuntime{PreviewEdits: preview, SubmitEdits: submit, EditKinds: []string{"update"}, PreviewEditKinds: []string{"update"}}, true},
		{"empty", DataRuntime{PreviewEdits: preview, SubmitEdits: submit, EditKinds: []string{}, PreviewEditKinds: []string{"update"}}, false},
		{"missing-submit", DataRuntime{PreviewEdits: preview, PreviewEditKinds: []string{"insert", "update", "delete"}}, false},
		{"missing-preview", DataRuntime{SubmitEdits: submit, PreviewEditKinds: []string{"insert", "update", "delete"}}, false},
		{"copy-only", DataRuntime{PreviewKeyCopy: copyPreview, SubmitEdits: submit, EditKinds: []string{"copy"}}, true},
		{"copy-missing", DataRuntime{PreviewEdits: preview, SubmitEdits: submit, EditKinds: []string{"copy"}, PreviewEditKinds: []string{"copy"}}, false},
		{"unknown-kind", DataRuntime{PreviewEdits: preview, SubmitEdits: submit, EditKinds: []string{"foreign"}, PreviewEditKinds: []string{"foreign"}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := tc.runtime
			r.Capabilities.EditableTables = true
			if HasDataWriteExecutor(r) != tc.want || BoundDataCapabilities(r).EditableTables != tc.want {
				t.Fatal("partial confirmation path advertised editing")
			}
			r.Capabilities.EditableTables = false
			if BoundDataCapabilities(r).EditableTables {
				t.Fatal("executor bypassed original capability")
			}
			r.Capabilities.EditableTables = true
			r.AsyncWrites = true
			if BoundDataCapabilities(r).EditableTables {
				t.Fatal("async admission bypassed verifier")
			}
		})
	}
}
