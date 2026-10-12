//go:build desktop

// agentbox-desktop is the agentbox CLI plus a native window for the dashboard.
// With arguments it is the CLI (so it can spawn itself as the daemon); with
// none it opens the window.
package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/mac"
	"github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/Ayush2966/Agent-Box/internal/api"
	"github.com/Ayush2966/Agent-Box/internal/cli"
	"github.com/Ayush2966/Agent-Box/internal/client"
	"github.com/Ayush2966/Agent-Box/internal/daemon"
	"github.com/Ayush2966/Agent-Box/internal/desktop"
)

// App is bound into the window as window.go.main.App.
type App struct {
	bridge atomic.Pointer[desktop.Bridge]
}

// StartLogs streams a box's log as "agentbox:log:<id>" events; the window
// chooses id and subscribes before calling (see desktop.Bridge.StartLogs).
func (a *App) StartLogs(id, repo, name string) error {
	b := a.bridge.Load()
	if b == nil {
		return fmt.Errorf("not connected yet")
	}
	return b.StartLogs(id, repo, name)
}

func (a *App) StopLogs(id string) {
	if b := a.bridge.Load(); b != nil {
		b.StopLogs(id)
	}
}

// ConnState lets the window learn the bridge's state when it subscribes,
// since conn events are only emitted on transitions.
func (a *App) ConnState() string {
	b := a.bridge.Load()
	if b == nil {
		return "connecting"
	}
	return b.State()
}

func main() {
	if !desktop.GUILaunch(os.Args[1:]) {
		os.Exit(cli.Execute())
	}
	home, err := daemon.DefaultHome()
	if err != nil {
		fatal(err)
	}
	p := daemon.PathsFor(home)
	// The widened PATH only matters for a daemon this process spawns (it
	// inherits our environment, and a Dock launch has launchd's minimal PATH),
	// so skip the login-shell probe when one is already running.
	if !daemon.Ping(p.Sock) {
		os.Setenv("PATH", desktop.ToolPATH(os.Getenv("PATH"), desktop.LoginPATH()))
	}
	c := client.New(p.Sock)
	apiAddr, token, err := desktop.Bootstrap(home, daemon.EnsureRunning, func(ctx context.Context, _ string) (api.HealthView, error) {
		return c.HealthInfo(ctx)
	})
	if err != nil {
		fatal(err)
	}
	proxy, err := desktop.NewProxy(apiAddr, token)
	if err != nil {
		fatal(err)
	}
	app := &App{}
	err = wails.Run(&options.App{
		Title:       "agentbox",
		Width:       1200,
		Height:      800,
		MinWidth:    900,
		MinHeight:   600,
		AssetServer: &assetserver.Options{Handler: proxy},
		Bind:        []interface{}{app},
		OnStartup: func(ctx context.Context) {
			b := desktop.NewBridge(c, func(name string, data any) { runtime.EventsEmit(ctx, name, data) })
			app.bridge.Store(b)
			go b.Run(ctx)
		},
		OnShutdown: func(context.Context) {
			if b := app.bridge.Load(); b != nil {
				b.Close()
			}
		},
		Mac: &mac.Options{About: &mac.AboutInfo{Title: "agentbox", Message: "Isolated dev environments for parallel AI coding agents"}},
	})
	if err != nil {
		fatal(err)
	}
}

// fatal reports a startup failure where a desktop user can see it, then exits.
func fatal(err error) {
	fmt.Fprintln(os.Stderr, "agentbox-desktop:", err)
	_ = exec.Command("osascript", "-e", fmt.Sprintf(`display dialog %s buttons {"OK"} default button 1 with title "agentbox" with icon stop`, appleScriptString(err.Error()))).Run()
	os.Exit(1)
}

// appleScriptString quotes s as an AppleScript string literal: only \ and "
// need escaping, and control characters (newlines included) become spaces.
func appleScriptString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '\\' || r == '"':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r < 0x20 || r == 0x7f:
			b.WriteByte(' ')
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}
