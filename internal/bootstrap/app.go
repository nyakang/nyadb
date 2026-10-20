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
		mygo.NewWindow(mygo.WindowOptions{
			Title:     "NyaDB",
			Width:     1280,
			Height:    800,
			MinWidth:  840,
			MinHeight: 560,
			StateKey:  "main",
			Content:   ui.View(state.View),
		})
	})
	return mygo.App.Run()
}
