package objectworkspace

import (
	"github.com/egoist/mygo/ui"
	"testing"
)

func TestTableProfileMenuUsesOpeningSnapshotAndRejectsGenerationChange(t *testing.T) {
	for _, stale := range []bool{false, true} {
		t.Run(map[bool]string{false: "snapshot", true: "generation"}[stale], func(t *testing.T) {
			s, b, session := browserFixture()
			value := "original"
			got := ""
			s.deps.TableProfiles = func(int64, string, string) ([]TableProfile, error) {
				return []TableProfile{{Name: "Focused", Value: value}}, nil
			}
			s.deps.OpenTableProfile = func(_ ui.Services, id int64, db, table, raw string) {
				if id != 7 || db != "app" || table != "orders" {
					t.Fatal("wrong target")
				}
				got = raw
			}
			tt, _ := browserTester(s, 900)
			if e := tt.RightClick("orders"); e != nil {
				t.Fatal(e)
			}
			value = "changed"
			if stale {
				session.Generation++
			}
			b.Objects[0].Comment = "new"
			tt.Frame()
			if e := tt.ChooseMenuItem("通过配置文件打开表", "Focused"); e != nil {
				t.Fatal(e)
			}
			if stale && got != "" {
				t.Fatal("stale menu applied profile")
			}
			if !stale && got != "original" {
				t.Fatalf("opening snapshot replaced: %q", got)
			}
		})
	}
}
