package kvworkspace

import (
	"fmt"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

func TestConsulToolsRetainVisibleActionsInSmallWindows(t *testing.T) {
	defer i18n.SetLocaleFunc(nil)
	for _, language := range []string{"zh-Hans", "en"} {
		for _, size := range [][2]int{{720, 520}, {1280, 800}} {
			for _, tool := range []string{"management", "archive", "bundle"} {
				t.Run(fmt.Sprintf("%s/%dx%d/%s", language, size[0], size[1], tool), func(t *testing.T) {
					i18n.SetLocaleFunc(func() string { return language })
					s, d, p := httpTestState(t)
					d.lease = &managementTestLease{fakeLease: p.lease}
					d.scope.HTTP.Protocol = "consul-http"
					var actions []string
					switch tool {
					case "management":
						s.OpenHTTPManagement(d)
						d.httpManagement.last = &contract.KVHTTPManagementResult{Receipt: contract.KVResult{HTTP: &contract.KVHTTPResult{Status: 200}}}
						for range 23 {
							d.httpManagement.capabilities = append(d.httpManagement.capabilities, contract.KVHTTPManagementResult{Request: contract.KVHTTPManagementRequest{Operation: "peering-list"}})
						}
						actions = []string{"探测 Consul 能力", "运行或预览管理请求", "打开私有输入", "保存私有输入", "导出原私有回执", "取消"}
					case "archive":
						d.httpArchive = &httpArchiveState{open: true, compressed: "67108864", expanded: "268435456", original: &contract.KVHTTPArchiveCapture{}, restoreLast: &contract.KVHTTPArchiveRestoreResult{}}
						actions = []string{"预览 Snapshot 捕获", "打开原 Snapshot 恢复文件", "预览 Snapshot 恢复", "清除原 Snapshot 归档", "取消"}
					case "bundle":
						s.OpenHTTPKVTools(d)
						actions = []string{"搜索", "捕获导出 Bundle", "打开 Bundle", "保存 Bundle", "预览 Bundle 导入", "预览下一独立事务", "导出完整工具记录", "取消"}
					}
					tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.Modals(c) }, size[0], size[1])
					tt.Frame()
					for _, action := range actions {
						r, ok := tt.Find(locales.Text(action))
						if !ok || r.W <= 0 || r.H <= 0 || r.X < 34 || r.Y < 34 || r.X+r.W > float32(size[0])-33 || r.Y+r.H > float32(size[1])-33 {
							t.Fatal("Consul tool action clipped", action, r)
						}
					}
					if dir := os.Getenv("NYADB_CONSUL_PUBLIC_IMAGES"); dir != "" {
						if err := os.MkdirAll(dir, 0700); err != nil {
							t.Fatal(err)
						}
						f, err := os.Create(filepath.Join(dir, fmt.Sprintf("%s-%dx%d-%s.png", language, size[0], size[1], tool)))
						if err != nil {
							t.Fatal(err)
						}
						err = png.Encode(f, tt.Image())
						closeErr := f.Close()
						if err != nil || closeErr != nil {
							t.Fatal("Consul tool screenshot failed")
						}
					}
				})
			}
		}
	}
}
