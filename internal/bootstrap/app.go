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
	// Research-only, opt-in runtime PNGs. The files remain outside resources/
	// and are never embedded in the application or its distribution packages.
	if os.Getenv("NYADB_RESEARCH_ICONS") == "1" {
		dir := os.Getenv("NYADB_RESEARCH_ICON_DIR")
		if dir == "" {
			dir = filepath.Join("temp", "research", "navicat-png-inventory", "named")
		}
		count, loadErr := state.LoadLocalResearchIcons(dir)
		fmt.Fprintf(os.Stderr, "NyaDB research icon mode: loaded %d icons\n", count)
		if loadErr != nil {
			fmt.Fprintf(os.Stderr, "NyaDB research icon warning: %v\n", loadErr)
		}
	}
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
		// Native parent-owned modal window, not an in-workspace overlay.
		// The OS provides the title bar, window movement and close button.
		var chooser *mygo.Window
		state.SetConnectionWindowHost(func() {
			if chooser != nil {
				chooser.Focus()
				return
			}
			chooserWidth, chooserHeight := state.ConnectionWindowSize()
			chooser = mygo.NewWindow(mygo.WindowOptions{
				Title:          state.ConnectionWindowTitle(),
				Parent:         window,
				Modal:          true,
				Width:          chooserWidth,
				Height:         chooserHeight,
				MinWidth:       720,
				MinHeight:      520,
				SkipTaskbar:    true,
				UseContentSize: true,
				Content:        ui.View(state.ConnectionWindowView),
			})
			chooser.OnClosed(func() {
				chooser = nil
				state.ConnectionWindowClosed()
				window.Update(func() {})
			})
		}, func() {
			if chooser != nil {
				chooser.Close()
			}
		})
		state.SetConnectionWindowTitleUpdater(func(title string) {
			if chooser != nil {
				chooser.SetTitle(title)
				width, height := state.ConnectionWindowSize()
				chooser.SetContentSize(width, height)
			}
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
