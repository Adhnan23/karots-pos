package escpos

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
)

// Send pipes raw bytes to the thermal printer through CUPS (`lp -o raw`). An
// empty printer name uses the system default destination. The queue must be a
// raw queue (no PPD) so CUPS passes the ESC/POS bytes through unmodified —
// installing a "driver" on the queue would re-introduce the PDF/raster problem.
func Send(ctx context.Context, printer string, data []byte) error {
	args := []string{"-o", "raw"}
	if printer != "" {
		args = append([]string{"-d", printer}, args...)
	}
	cmd := exec.CommandContext(ctx, "lp", args...)
	cmd.Stdin = bytes.NewReader(data)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("lp failed: %w: %s", err, bytes.TrimSpace(out))
	}
	return nil
}
