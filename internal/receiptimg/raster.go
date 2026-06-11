// Package receiptimg renders the bits of a thermal receipt that the printer's
// built-in font cannot draw — the shop logo and the secondary (non-Latin) shop
// name — into ESC/POS raster blocks (GS v 0). The output is embedded directly
// in the receipt byte stream by internal/escpos.
package receiptimg

// PrinterDots is the print head width in dots for the configured paper. 80mm
// POS printers are 576 dots (72mm @ 203dpi); 58mm are 384.
func PrinterDots(width string) int {
	if width == "58" {
		return 384
	}
	return 576
}

// rasterFromInk encodes a w×h 1-bit bitmap as an ESC/POS GS v 0 raster command.
// ink(x,y) reports whether a dot should be black. Rows are packed MSB-first,
// ceil(w/8) bytes per row.
func rasterFromInk(w, h int, ink func(x, y int) bool) []byte {
	if w <= 0 || h <= 0 {
		return nil
	}
	bytesPerRow := (w + 7) / 8
	out := make([]byte, 0, 8+bytesPerRow*h)
	out = append(out, 0x1D, 'v', '0', 0,
		byte(bytesPerRow%256), byte(bytesPerRow/256),
		byte(h%256), byte(h/256))
	for y := 0; y < h; y++ {
		for bx := 0; bx < bytesPerRow; bx++ {
			var v byte
			for bit := 0; bit < 8; bit++ {
				if x := bx*8 + bit; x < w && ink(x, y) {
					v |= 1 << (7 - bit)
				}
			}
			out = append(out, v)
		}
	}
	return out
}
