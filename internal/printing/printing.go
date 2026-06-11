// Package printing is the transport layer shared by the receipt (ESC/POS) and
// label (TSPL) renderers. It sends raw bytes to a CUPS queue and discovers the
// queues available on the host so the admin can map "which printer for which"
// from the Settings UI.
package printing

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// Raw pipes bytes straight to a CUPS queue (`lp -o raw`). An empty queue name
// uses the system default destination. The queue MUST be a raw queue (no PPD /
// driver) so CUPS passes the bytes through unmodified — installing a driver on
// the queue makes CUPS rasterize to PDF, which thermal printers mis-print as
// garbage (the original receipt-printer bug).
func Raw(ctx context.Context, queue string, data []byte) error {
	args := []string{"-o", "raw"}
	if queue != "" {
		args = append([]string{"-d", queue}, args...)
	}
	cmd := exec.CommandContext(ctx, "lp", args...)
	cmd.Stdin = bytes.NewReader(data)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("lp failed: %w: %s", err, bytes.TrimSpace(out))
	}
	return nil
}

// Queues returns the names of the CUPS print queues on this host (`lpstat -e`),
// used to populate the printer dropdowns in Settings. A missing/failed lpstat
// returns an empty list rather than an error so the Settings page still renders.
func Queues(ctx context.Context) []string {
	out, err := exec.CommandContext(ctx, "lpstat", "-e").Output()
	if err != nil {
		return nil
	}
	var names []string
	for ln := range strings.SplitSeq(string(out), "\n") {
		if s := strings.TrimSpace(ln); s != "" {
			names = append(names, s)
		}
	}
	return names
}
