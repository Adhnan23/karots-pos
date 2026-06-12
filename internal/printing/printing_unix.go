//go:build !windows

package printing

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// rawSpool pipes bytes straight to a CUPS queue (`lp -o raw`). An empty queue
// name uses the system default destination. The queue MUST be a raw queue (no
// PPD/driver) so CUPS passes the bytes through unmodified.
func rawSpool(ctx context.Context, queue string, data []byte) error {
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

// osQueues lists the CUPS print queues on this host (`lpstat -e`). A
// missing/failed lpstat returns nil.
func osQueues(ctx context.Context) []string {
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
