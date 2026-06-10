# Receipt printing

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

2. Tell the app which queue to use (optional). Set in `.env`:
   ```
   RECEIPT_PRINTER=POS80
   ```
   If unset/empty, the **system default printer** is used.

That's it — no `about:config`, no `--kiosk-printing`, no browser print settings.

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
