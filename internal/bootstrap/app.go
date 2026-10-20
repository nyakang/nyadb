// Package bootstrap wires the native MyGo desktop application.
package bootstrap

import (
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/ui/shell"
)

// Run starts the main OS-thread event loop; call it from main.
func Run() error {
	state := shell.New()
	mygo.App.WhenReady(func() {
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
