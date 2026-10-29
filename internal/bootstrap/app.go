// Package bootstrap wires the native MyGo desktop application.
package bootstrap

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/credentials"
	"github.com/nyakang/nyadb/internal/storage/localdb"
	"github.com/nyakang/nyadb/internal/ui/shell"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

// Run starts the main OS-thread event loop; call it from main.
func Run() error {
	// Keep Windows 11's native HMENU interaction but use a denser layout,
	// matching the compact menus already configured for Linux GTK.
	// This must be configured before App.Run initializes the backend.
	mygo.App.SetMenuDensity(mygo.MenuCompact)
	configDir, err := os.UserConfigDir()
	if err != nil {
		return fmt.Errorf("resolve NyaDB configuration directory: %w", err)
	}
	db, err := localdb.Open(filepath.Join(configDir, "NyaDB", "config.sqlite"))
	if err != nil {
		return fmt.Errorf("open NyaDB profile database: %w", err)
	}
	defer db.Close()
	savedAppearance, err := localdb.Preference(db, "appearance")
	if err != nil {
		return fmt.Errorf("read appearance preference: %w", err)
	}
	theme.SetMode(theme.ParseMode(savedAppearance))
	state := shell.NewWithProfiles(connections.NewService(localdb.NewProfileStore(db)))
	state.SetCredentialVault(credentials.NewOSVault())
	state.SetDataLayoutPreferences(
		func(key string) (string, error) { return localdb.Preference(db, key) },
		func(key, value string) error { return localdb.SetPreference(db, key, value) },
	)
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
		setNativeAppearance(theme.CurrentMode())
		window := mygo.NewWindow(mygo.WindowOptions{
			Title:     "NyaDB",
			Width:     1280,
			Height:    800,
			MinWidth:  840,
			MinHeight: 560,
			StateKey:  "main",
			Content:   ui.View(state.View),
		})
		window.OnClose(func(e *mygo.CloseEvent) {
			if state.RequestWorkbenchClose(func() { window.Close() }) {
				e.PreventDefault()
				window.Update(func() {})
			}
		})
		window.OnClosed(func() { state.CloseMySQLSessions() })
		// Native parent-owned modal window, not an in-workspace overlay.
		// The OS provides the title bar, window movement and close button.
		var chooser *mygo.Window
		refreshWindows := func() {
			window.Update(func() {})
			if chooser != nil {
				chooser.Update(func() {})
			}
		}
		state.SetAppearanceHandler(func(mode theme.Mode) {
			if err := localdb.SetPreference(db, "appearance", string(mode)); err != nil {
				fmt.Fprintf(os.Stderr, "NyaDB appearance preference: %v\n", err)
			}
			setNativeAppearance(mode)
			refreshWindows()
		})
		mygo.Theme.OnUpdated(func() {
			applyNativeGTKTheme(theme.CurrentMode())
			refreshWindows()
		})
		updateChooserIcon := func() {
			if chooser == nil {
				return
			}
			icon, err := state.ConnectionWindowIcon()
			if err == nil {
				err = chooser.SetIcon(icon)
			}
			if err != nil {
				fmt.Fprintf(os.Stderr, "NyaDB connection icon: %v\n", err)
			}
		}
		mygo.App.OnLocaleChanged(func(string) {
			if chooser != nil {
				chooser.SetTitle(state.ConnectionWindowTitle())
			}
		})
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
			updateChooserIcon()
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
				updateChooserIcon()
			}
		})
		state.SetSQLiteFilePicker(func() {
			go func() {
				paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{
					Parent: window,
					Title:  locales.Text("选择现有 SQLite 数据库文件"),
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
		state.SetMySQLSettingsPicker(func() {
			go func() {
				paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{
					Parent:    chooser,
					Title:     locales.Text("选择 MySQL 设置位置"),
					Directory: true,
				})
				window.Update(func() {
					if err != nil {
						state.MySQLSettingsPickerError()
						return
					}
					if len(paths) != 0 {
						state.SelectMySQLSettingsDirectory(paths[0])
					}
				})
			}()
		})
		state.SetMySQLSSLFilePicker(func(kind shell.MySQLSSLFile) {
			// Capture the editing session. Discard a late selection if the
			// modal was closed and a different profile has since opened.
			version := state.MySQLSSLSessionVersion()
			parent := chooser
			if parent == nil {
				return
			}
			title := locales.Text("选择 SSL 文件")
			switch kind {
			case shell.MySQLSSLClientKey:
				title = locales.Text("选择客户端密钥")
			case shell.MySQLSSLClientCertificate:
				title = locales.Text("选择客户端证书")
			case shell.MySQLSSLCA:
				title = locales.Text("选择 CA 证书")
			}
			go func() {
				paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{
					Parent: parent,
					Title:  title,
				})
				window.Update(func() {
					if err != nil {
						state.MySQLSSLFilePickerError(version)
						return
					}
					if len(paths) > 0 {
						state.SelectMySQLSSLFile(kind, version, paths[0])
					}
				})
			}()
		})
		state.SetMySQLSSHKeyPicker(func() {
			version := state.MySQLSSHSessionVersion()
			parent := chooser
			if parent == nil {
				return
			}
			go func() {
				paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{
					Parent: parent,
					Title:  locales.Text("选择 SSH 私钥"),
				})
				window.Update(func() {
					if err != nil {
						state.MySQLSSHKeyPickerError(version)
						return
					}
					if len(paths) > 0 {
						state.SelectMySQLSSHPrivateKey(version, paths[0])
					}
				})
			}()
		})
		state.SetMySQLHTTPFilePicker(func(kind shell.MySQLHTTPFile) {
			version := state.MySQLHTTPSessionVersion()
			parent := chooser
			if parent == nil {
				return
			}
			title := locales.Text("选择 HTTP 证书文件")
			switch kind {
			case shell.MySQLHTTPClientKey:
				title = locales.Text("选择 HTTP 客户端密钥")
			case shell.MySQLHTTPClientCertificate:
				title = locales.Text("选择 HTTP 客户端证书")
			case shell.MySQLHTTPCA:
				title = locales.Text("选择 HTTP CA 证书")
			}
			go func() {
				paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{
					Parent: parent,
					Title:  title,
				})
				window.Update(func() {
					if err != nil {
						state.MySQLHTTPFilePickerError(version)
						return
					}
					if len(paths) > 0 {
						state.SelectMySQLHTTPFile(kind, version, paths[0])
					}
				})
			}()
		})
	})
	return mygo.App.Run()
}

func setNativeAppearance(mode theme.Mode) {
	applyNativeGTKTheme(mode)
	switch mode {
	case theme.ModeLight:
		mygo.Theme.SetSource(mygo.ThemeLight)
	case theme.ModeSystem:
		mygo.Theme.SetSource(mygo.ThemeSystem)
	default:
		mygo.Theme.SetSource(mygo.ThemeDark)
	}
}
