package web

import (
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"github.com/labstack/echo/v4"
)

// kioskSentinel returns the file the kiosk relaunch loop watches: when it exists
// after the browser quits, the loop breaks and stays on the desktop instead of
// reopening the till. It MUST match the path baked into the launcher for this OS
// — kiosk.sh (Linux) uses /tmp, kiosk.cmd (Windows) uses %TEMP%. The server and
// the launcher run as the same desktop user, so both resolve to the same file.
func kioskSentinel() string {
	if runtime.GOOS == "windows" {
		return filepath.Join(os.TempDir(), "karots-kiosk-exit")
	}
	return "/tmp/karots-kiosk-exit"
}

// killKiosk signals the kiosk browser to quit — precisely, matching the --kiosk
// flag so a support user's own browser windows are left alone. Best-effort: no
// kiosk running (dev, or already closed) is not an error.
//
// ponytail: matches on the "--kiosk" flag, so it's fine for one kiosk per box;
// revisit only if a machine ever runs two.
func killKiosk() {
	if runtime.GOOS == "windows" {
		// taskkill can't filter on the command line, so find the kiosk browser by
		// its --kiosk arg via CIM and stop just those PIDs.
		const ps = `Get-CimInstance Win32_Process -Filter "Name='chrome.exe' OR Name='chromium.exe' OR Name='msedge.exe'" | ` +
			`Where-Object { $_.CommandLine -like '*--kiosk*' } | ` +
			`ForEach-Object { Stop-Process -Id $_.ProcessId -Force }`
		_ = exec.Command("powershell", "-NoProfile", "-Command", ps).Run()
		return
	}
	// The "[-]-kiosk" regex matches the "--kiosk" flag but not pkill's own cmdline.
	_ = exec.Command("pkill", "-f", "[-]-kiosk").Run()
}

// KioskExit lets an admin or the support account drop the kiosk to the desktop.
// The browser can't be closed by Alt+F4 (the relaunch loop reopens it) nor from
// the page (window.close() is unreliable in --app kiosk), so the server — which
// runs as the same desktop user — writes the loop's exit sentinel and then
// signals the browser to quit. Gated by RequireKioskExit; a no-op on a dev box
// with no kiosk.
func (h *authUI) KioskExit(c echo.Context) error {
	// Sentinel first, so the loop already sees "quit" the instant the browser dies.
	if err := os.WriteFile(kioskSentinel(), []byte("1"), 0o644); err != nil {
		return err
	}
	killKiosk()
	return c.NoContent(http.StatusOK)
}
