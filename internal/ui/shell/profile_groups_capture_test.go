package shell

import (
	"context"
	"fmt"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/ui/navigation"
)

// Optional visual evidence: this is an isolated, synthetic local profile list,
// rendered by MyGo headless. It is not a Navicat or real-desktop screenshot.
func TestCaptureGroupedWorkbenchPreview(t *testing.T) {
	if os.Getenv("NYADB_GROUPED_CAPTURE") != "1" {
		t.Skip("set NYADB_GROUPED_CAPTURE=1 to capture the synthetic grouped workbench")
	}
	s, _, service, _ := profileTestState(t)
	ctx := context.Background()
	if err := service.CreateGroup(ctx, "开发库"); err != nil {
		t.Fatal(err)
	}
	if err := service.CreateGroup(ctx, "生产库"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		_, err := service.Create(ctx, connections.Profile{
			Name:      fmt.Sprintf("development-%02d", i+1),
			GroupName: "开发库",
			Kind:      connections.MySQL,
			Host:      "example.invalid",
			Port:      3306,
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	s.loadProfiles()
	s.selectGroup("开发库")
	s.navigation.Tree.Open.Add(navigation.GroupKey("开发库"))
	tt := ui.NewTester(s.View, 1280, 800)
	path := filepath.Join("..", "..", "..", "temp", "research", "main-workbench-grouped-20261022", "nyadb-headless-groups.png")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, tt.Image()); err != nil {
		t.Fatal(err)
	}
}
