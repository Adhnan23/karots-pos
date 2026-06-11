# Printing (receipts & labels)

Receipts print **server-side as ESC/POS** straight from the Go app to the thermal
printer. The browser is not involved — no print dialog, no PDF, no driver. This is
the reliable path for cheap thermal printers (Xprinter POS-80 etc.).

## How it works

- The cashier's **Print Bill** button (and the **Reprint** button on
  `/cashier/receipts`) call `POST /cashier/print/:id`.
- The handler builds the receipt as ESC/POS bytes (`internal/escpos`) using the
  printer's **built-in font** — sized to the shop's paper width, with header/totals
  formatting and an automatic **feed + partial cut** at the end.
- Bytes are sent to CUPS with `lp -o raw` (`internal/escpos/printer.go`).

Because it uses the built-in font and an explicit cut, the receipt is the exact
length of its content (no A4, no 1-metre over-feed) and prints the correct
characters (no CJK garbage).

## Paper width

Admin → Settings → **Receipt Paper Width** (`80mm` = 48 cols, `58mm` = 32 cols),
stored in `settings.receipt_width`. The ESC/POS layout adapts automatically. The
HTML receipt page (`/cashier/receipt/:id`, for on-screen viewing / non-thermal
printers) uses the same setting via a per-page `@page { size }`.

## Printer setup (one-time)

1. The CUPS queue **must stay a *raw* queue** (no PPD/driver). A raw queue passes
   the ESC/POS bytes through unchanged. *Installing a "driver" on it re-introduces
   the PDF/raster problem that printed garbage.*

   Create one if needed (USB example):
   ```
   lpadmin -p POS80 -E -v usb://Printer/POS-80 -m raw
   lpadmin -d POS80          # make it the default
   ```

2. Tell the app which queue to use. **Admin → Settings → Printers & Labels** has a
   **Receipt printer** dropdown (auto-filled from the queues CUPS reports). Pick the
   receipt queue there. The `RECEIPT_PRINTER` env var in `.env` is only a fallback
   used when that setting is left on "System default":
   ```
   RECEIPT_PRINTER=POS80
   ```
   If both are empty, the **system default printer** is used.

That's it — no `about:config`, no `--kiosk-printing`, no browser print settings.

## Barcode label printing

Barcode price labels print the same way: **server-side, raw, no browser**. A label
printer (e.g. **Xprinter XP-365B**) speaks **TSPL**, not ESC/POS, so it has its own
renderer (`internal/tspl`) — but the transport is shared (`internal/printing`, the
same `lp -o raw`). The printer's built-in `BARCODE` command draws the (scannable)
barcode, so there's no image library and the binary stays self-contained.

- **Admin → Barcode Labels** and **Terminal → Labels** (cashiers too): pick a
  product or type a custom code, choose the **sticker size**, and hit
  **🖨 Print to label printer** → `POST /admin/labels/send` / `POST /cashier/labels/send`.
- The HTML **Generate sheet ↗** button is still there as an A4 sticker-sheet
  fallback (browser print).

### Setup (one-time)

1. Add a **raw** queue for the label printer (same raw rule as the receipt printer —
   a driver re-introduces the PDF/garbage problem):
   ```
   lpadmin -p XP365 -E -v 'usb://Xprinter/XP-365B?serial=XXXX' -m raw
   ```
   Find the exact device URI with `lpinfo -v`. (CUPS warns that raw queues are
   deprecated; they still work. The alternative is a queue with the vendor's
   PPD set to send data unfiltered.)
2. **Admin → Settings → Printers & Labels**: set the **Label printer** dropdown to
   that queue, and the **default label size** (default 50 × 25 mm). `LABEL_PRINTER`
   in `.env` is the fallback when the setting is "System default".

### Sticker size

The size is chosen **per print** on the Labels page — presets (50×25, 50×30, 40×30,
38×25, 100×50) or **Custom…** (width / height / gap in mm). "Default" uses the size
saved in Settings (`settings.label_width_mm` / `label_height_mm` / `label_gap_mm`).

## Non-ASCII text

The built-in font prints Latin/ASCII. Non-Latin shop text (e.g. Sinhala shop name)
is replaced with `?` in the thermal output to avoid garbage; use the HTML receipt
page if you need to display/print that. Receipt numbers, dates, and amounts are
ASCII and print correctly.

## Troubleshooting

- **Nothing prints / `lp failed`** — check `lpstat -p` (queue enabled?),
  `lpstat -o` (stuck jobs?), and that the printer is powered/connected.
- **Garbage characters or huge feed return** — the queue is no longer raw (a driver
  got installed). Recreate it with `-m raw`.
- Inspect what was sent: a correct job is small (~1 KB in `lpstat -W completed -o`);
  a ~15–20 KB job means a PDF was sent (wrong path).
