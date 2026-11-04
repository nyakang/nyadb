package theme

import "github.com/egoist/mygo/ui"

// EditTarget is a window-local command endpoint. Its owner controls lifetime;
// hosts must recheck Valid before dispatching a menu's captured target.
type EditTarget struct {
	ID      string
	Handle  *ui.Handle
	Native  bool
	Valid   func() bool
	Enabled func(string) bool
	Run     func(ui.Services, string) bool
}

type EditMenuEntry struct {
	Label, Command  string
	Enabled, Native bool
	Run             func()
}

func (t EditTarget) Available(command string) bool {
	return t.ID != "" && t.Valid != nil && t.Valid() && t.Enabled != nil && t.Enabled(command)
}

func (t EditTarget) Execute(services ui.Services, command string) bool {
	return t.Available(command) && t.Run != nil && t.Run(services, command)
}
