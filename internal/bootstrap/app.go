// Package bootstrap wires the native MyGo desktop application.
package bootstrap

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/credentials"
	"github.com/nyakang/nyadb/internal/services/datasources"
	"github.com/nyakang/nyadb/internal/services/queries"
	"github.com/nyakang/nyadb/internal/storage/localdb"
	"github.com/nyakang/nyadb/internal/ui/objectworkspace"
	"github.com/nyakang/nyadb/internal/ui/shell"
	"github.com/nyakang/nyadb/internal/ui/sqlworkspace"
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
	if err := loadAppearance(db); err != nil {
		return err
	}
	state := shell.NewWithProfiles(connections.NewService(localdb.NewProfileStore(db)))
	state.SetCredentialVault(credentials.NewOSVault())
	hosts, err := credentials.DefaultHostStore()
	if err != nil {
		return fmt.Errorf("resolve SSH host trust storage: %w", err)
	}
	state.EnableTunnelServices(credentials.NewTunnelStore(true), hosts)
	state.SetDataSources(datasources.Open, datasources.Capabilities)
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
	queryStore := queries.NewStore(localdb.NewSQLRepository(db), credentials.WorkspaceKey{})
	state.SetInitialSQLStore(queryStore)
	state.EnableAutoConnect()
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
		state.SetSQLPersistence(queryStore, sqlworkspace.Files{
			OpenPath: func() (string, error) {
				paths, e := mygo.Dialog.Open(mygo.OpenDialogOptions{Parent: window, Title: locales.Text("打开 SQL 文件"), Filters: []mygo.FileFilter{{Name: "SQL", Extensions: []string{"sql"}}}})
				if e != nil || len(paths) == 0 {
					return "", e
				}
				return paths[0], nil
			},
			SavePath: func(name string) (string, error) {
				return mygo.Dialog.Save(mygo.SaveDialogOptions{Parent: window, Title: locales.Text("保存 SQL 文件"), DefaultPath: name})
			},
			OpenReader: func(path string) (io.ReadCloser, error) { return os.Open(path) },
			Read:       queries.ReadFile, Write: queries.WriteFile, WriteStream: queries.WriteStream,
		}, func() { window.Update(func() {}) })

		state.SetBackupFiles(objectworkspace.BackupFiles{
			Open: func() (string, error) {
				paths, e := mygo.Dialog.Open(mygo.OpenDialogOptions{Parent: window, Title: locales.Text("恢复 NyaDB 备份"), Filters: []mygo.FileFilter{{Name: "NyaDB Backup", Extensions: []string{"nyabackup"}}}})
				if e != nil || len(paths) == 0 {
					return "", e
				}
				return paths[0], nil
			},
			Save: func(name string) (string, error) {
				return mygo.Dialog.Save(mygo.SaveDialogOptions{Parent: window, Title: locales.Text("保存备份"), DefaultPath: name})
			}, Write: queries.WriteStream,
		})
		state.SetEngineFiles(func(name string) (string, error) {
			return mygo.Dialog.Save(mygo.SaveDialogOptions{Parent: window, DefaultPath: name})
		}, queries.WriteStream)
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
		state.SetThemePreferenceSaver(func(key, value string) error {
			err := localdb.SetPreference(db, key, value)
			if err != nil {
				fmt.Fprintf(os.Stderr, "NyaDB appearance preference: %v\n", err)
			}
			return err
		})
		state.SetAppearanceHandler(func(mode theme.Mode) {
			setNativeAppearance(mode)
			refreshWindows()
		})
		state.SetColorSchemeHandler(func(theme.Scheme) { refreshWindows() })
		state.SetMetricsHandler(func(theme.MetricsPreferences) { refreshWindows() })
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
