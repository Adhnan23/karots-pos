package escpos

import (
	"context"

	"karots-pos/internal/printing"
)

// Send pipes raw ESC/POS bytes to the thermal printer through CUPS. An empty
// printer name uses the system default destination. The queue must be a raw
// queue (no PPD) so CUPS passes the bytes through unmodified — installing a
// "driver" on the queue would re-introduce the PDF/raster problem. The actual
// transport lives in internal/printing (shared with the TSPL label renderer).
func Send(ctx context.Context, printer string, data []byte) error {
	return printing.Raw(ctx, printer, data)
}
