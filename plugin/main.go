// Command plugin is the navidrome-mood Extism plugin.
//
// Build (standard Go, not TinyGo - TinyGo works but is not required, and standard
// Go gives the full stdlib):
//
//	GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -o plugin.wasm .
//
// Package it by zipping plugin.wasm together with manifest.json into a .ndp.
//
// # The 30-second ceiling shapes the whole design
//
// plugins/manager.go sets defaultTimeout = 30s, hands it to Extism, and wazero
// enforces it. There is no configuration knob, and it applies to every entry point
// including nd_task_execute and nd_scheduler_callback. So a whole-library pass
// cannot be one long call: it is many small tasks on the taskqueue host service,
// which is SQLite-backed and survives restarts.
package main

import (
	"github.com/navidrome/navidrome/plugins/pdk/go/lifecycle"
	"github.com/navidrome/navidrome/plugins/pdk/go/scheduler"
	"github.com/navidrome/navidrome/plugins/pdk/go/taskworker"
)

// plugin implements the capabilities Navidrome infers from the exported wasm
// functions. Capabilities are NOT declared in manifest.json - the host discovers
// them by looking for the nd_* exports the PDK generates from these interfaces.
type plugin struct{}

var (
	_ lifecycle.InitProvider         = (*plugin)(nil)
	_ taskworker.TaskExecuteProvider = (*plugin)(nil)
	_ scheduler.CallbackProvider     = (*plugin)(nil)
)

func init() {
	p := &plugin{}
	lifecycle.Register(p)
	taskworker.Register(p)
	scheduler.Register(p)
}

// main is required for a Go wasm build but never runs as an entry point; the host
// calls the exported nd_* functions directly.
func main() {}
