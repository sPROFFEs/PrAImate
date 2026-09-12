// praimate-gui is the Wails desktop shell around internal/core. It is
// a separate Go module from the root so `go build ./...` at the repo
// root never needs webkit2gtk headers; build this binary with:
//
//	cd cmd/praimate-gui
//	(cd frontend && pnpm install && pnpm build)
//	go build -tags desktop,production,webkit2_41 -o praimate-gui .
//
// On Windows the webkit2_41 tag is ignored; on Linux it selects
// webkit2gtk-4.1 (modern distros no longer ship 4.0). macOS is not a
// supported PrAImate platform.
package main

import (
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/linux"

	"git.jtsec.local/lab/PrAImate/internal/installer"
)

//go:embed all:frontend/dist
var assets embed.FS

// appIcon is the window/taskbar icon. On Linux Wails sets it at
// runtime via linux.Options.Icon; on Windows the icon comes from the
// exe's embedded resource (rsrc_windows_amd64.syso, generated with
// go-winres from this same PNG — see build.sh).
//
//go:embed frontend/src/assets/monke-icon.png
var appIcon []byte

func main() {
	if !supportedDesktopOS(runtime.GOOS) {
		fmt.Fprintln(os.Stderr, "PrAImate supports Linux and Windows only")
		os.Exit(1)
	}

	// Hidden mode: when claude spawns this same binary as the MCP
	// approval shim (`praimate-gui --mcp-approve <url> --mcp-token <tok>`),
	// run the stdio server and exit WITHOUT touching Wails — no window,
	// no webview, just stdin/stdout JSON-RPC.
	if len(os.Args) >= 3 && (os.Args[1] == "--mcp-approve" || os.Args[1] == "-mcp-approve") {
		endpoint := os.Args[2]
		token := ""
		for i := 3; i+1 < len(os.Args); i++ {
			if os.Args[i] == "--mcp-token" || os.Args[i] == "-mcp-token" {
				token = os.Args[i+1]
			}
		}
		os.Exit(runApprovalShim(os.Stdin, os.Stdout, endpoint, token))
	}

	// Hidden mode: when a CLI spawns this same binary as the MCP
	// skills provider (`praimate-gui --mcp-skills <url> --mcp-token <tok>`),
	// run the stdio server and exit WITHOUT touching Wails.
	if len(os.Args) >= 3 && (os.Args[1] == "--mcp-skills" || os.Args[1] == "-mcp-skills") {
		endpoint := os.Args[2]
		token := ""
		for i := 3; i+1 < len(os.Args); i++ {
			if os.Args[i] == "--mcp-token" || os.Args[i] == "-mcp-token" {
				token = os.Args[i+1]
			}
		}
		os.Exit(runSkillsShim(os.Stdin, os.Stdout, endpoint, token))
	}
	// WebKitGTK's accelerated compositing misorders layers on machines
	// with broken GPU drivers (VMs especially): composited editor
	// content paints OVER fixed overlays regardless of z-index. CPU
	// rendering is plenty for this UI — disable compositing outright.
	// No-op on Windows (different webview).
	if runtime.GOOS == "linux" && os.Getenv("WEBKIT_DISABLE_COMPOSITING_MODE") == "" {
		_ = os.Setenv("WEBKIT_DISABLE_COMPOSITING_MODE", "1")
	}

	// PATH hydration — when launched from a desktop shortcut / dock /
	// Start menu, the process PATH is the desktop session's, not the
	// user's shell. Eagerly prepend every known PrAImate-managed and
	// user-level CLI install dir so exec.LookPath finds bun, pnpm,
	// cargo, deno, claude, etc., without a shell relog.
	installer.ImportPnpmPathIfPresent()
	installer.ImportManagedAgentsToPath()
	installer.ImportManagedToolsToPath()
	installer.ImportPraimateBinToPath()
	installer.ImportUserBinDirs()

	if runtime.GOOS == "linux" && len(appIcon) > 0 {
		ensureLinuxDesktopIcon()
	}

	// Studio mode: `praimate-gui --editor <folder> --editor-chat <id>`
	// opens the document-studio window instead of the main app (Wails
	// v2 has one window per process — see editor_window.go).
	title := "PrAImate"
	if len(os.Args) >= 2 && (os.Args[1] == "--detached-window" || os.Args[1] == "-detached-window") {
		detachedProcessMode = detachedModeFromEnvironment()
		if !detachedProcessMode.active {
			fmt.Fprintln(os.Stderr, "invalid detached-window environment")
			os.Exit(2)
		}
		title = "PrAImate — " + detachedProcessMode.title
	}
	if len(os.Args) >= 3 && (os.Args[1] == "--editor" || os.Args[1] == "-editor") {
		editorFolder = os.Args[2]
		for i := 3; i+1 < len(os.Args); i++ {
			if os.Args[i] == "--editor-chat" || os.Args[i] == "-editor-chat" {
				editorChatID = os.Args[i+1]
			}
		}
		title = "PrAImate Studio — " + editorFolder
	}

	app := NewApp()

	err := wails.Run(&options.App{
		Title:  title,
		Width:  1280,
		Height: 820,
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		BackgroundColour: &options.RGBA{R: 16, G: 18, B: 24, A: 255},
		OnStartup:        app.startup,
		OnShutdown:       app.shutdown,
		OnBeforeClose:    app.beforeClose,
		Bind: []interface{}{
			app,
		},
		Linux: &linux.Options{
			ProgramName: "praimate",
			Icon:        appIcon,
		},
	})
	if err != nil {
		panic(err)
	}
}

func supportedDesktopOS(goos string) bool {
	return goos == "linux" || goos == "windows"
}

func ensureLinuxDesktopIcon() {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return
	}
	iconDirs := []string{
		filepath.Join(home, ".local", "share", "icons", "hicolor", "512x512", "apps"),
		filepath.Join(home, ".local", "share", "pixmaps"),
		filepath.Join(home, ".local", "share", "icons"),
	}
	for _, dir := range iconDirs {
		_ = os.MkdirAll(dir, 0755)
		target := filepath.Join(dir, "praimate.png")
		if _, err := os.Stat(target); os.IsNotExist(err) {
			_ = os.WriteFile(target, appIcon, 0644)
		}
	}
}
