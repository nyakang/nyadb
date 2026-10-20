// Package bootstrap wires the native MyGo desktop application.
package bootstrap

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/storage/localdb"
	"github.com/nyakang/nyadb/internal/ui/shell"
)

// Run starts the main OS-thread event loop; call it from main.
func Run() error {
	configDir, err := os.UserConfigDir()
	if err != nil {
		return fmt.Errorf("resolve NyaDB configuration directory: %w", err)
	}
	db, err := localdb.Open(filepath.Join(configDir, "NyaDB", "config.sqlite"))
	if err != nil {
		return fmt.Errorf("open NyaDB profile database: %w", err)
	}
	defer db.Close()
	state := shell.NewWithProfiles(connections.NewService(connections.NewStore(db)))
	mygo.App.WhenReady(func() {
		mygo.Theme.SetSource(mygo.ThemeDark)
		window := mygo.NewWindow(mygo.WindowOptions{
			Title:     "NyaDB",
			Width:     1280,
			Height:    800,
			MinWidth:  840,
			MinHeight: 560,
			StateKey:  "main",
			Content:   ui.View(state.View),
		})
		state.SetSQLiteFilePicker(func() {
			go func() {
				paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{
					Parent: window,
					Title:  "选择现有 SQLite 数据库文件",
					Filters: []mygo.FileFilter{
						{Name: "SQLite databases", Extensions: []string{"db", "sqlite", "sqlite3"}},
					},
				})
				window.Update(func() {
					if err != nil {
						state.SQLiteFilePickerError()
						return
					}
					if len(paths) > 0 {
						state.SelectSQLiteFile(paths[0])
					}
				})
			}()
		})
	})
	return mygo.App.Run()
}
