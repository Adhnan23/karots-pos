# Printing (receipts & labels)

> First-time installation (including the quick printer setup) lives in **`SETUP.md`**.
> This document is the in-depth reference: paper widths, labels/TSPL, logo
> rasterization, and full troubleshooting.

Receipts print **server-side as ESC/POS** straight from the Go app to the thermal
printer. The browser is not involved — no print dialog, no PDF, no driver. This is
the reliable path for cheap thermal printers (Xprinter POS-80 etc.).

## How it works

- The cashier's **Print Bill** button (and the **Reprint** button on
  `/cashier/receipts`) call `POST /cashier/print/:id`.
- The handler builds the receipt as ESC/POS bytes (`internal/escpos`) using the
  printer's **built-in font** — sized to the shop's paper width, with header/totals
  formatting and an automatic **feed + partial cut** at the end.
- Bytes are sent to the printer raw, untouched, via `internal/printing`. The
  transport is platform-specific behind build tags: **Linux** uses CUPS (`lp -o
  raw` / `lpstat -e`, `printing_unix.go`); **Windows** uses the print spooler's RAW
  datatype via `winspool.drv` (`printing_windows.go`); and a **`tcp://host:9100`**
  target sends straight to a network printer over a socket on any OS. The renderers
  (`internal/escpos`, `internal/tspl`, `internal/receiptimg`) are OS-independent —
  only the ~transport differs per platform.

Because it uses the built-in font and an explicit cut, the receipt is the exact
length of its content (no A4, no 1-metre over-feed) and prints the correct
characters (no CJK garbage).

By default, completing a sale shows a **Print / New Sale** prompt. You can turn
this off in **Admin → Settings → "Ask to print after each sale"**: with it off, a
completed sale prints the receipt automatically and resets for the next customer.

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
   **Receipt printer** field (auto-suggests the printers the OS reports). Pick the
   receipt queue there. Leave it blank to use the **system default printer**. There
   is no environment variable — printer selection lives entirely in Settings.

That's it — no `about:config`, no `--kiosk-printing`, no browser print settings.

## Per-cashier printer (multiple counters on a LAN)

When several cashiers each run the web terminal from their own PC (one binary + DB
on the admin PC, the others connecting over the LAN), each counter usually needs
its own receipt printer. Printing is server-side, so the target is resolved on the
server **per logged-in cashier** (by account, not by PC), in this order:

1. The cashier is identified from their login.
2. If that user has a **Receipt printer** set on their account
   (**Admin → Users → edit user → "Receipt printer (this counter)"**), it wins.
3. Otherwise the shop-wide **Admin → Settings → Receipt printer** is used.
4. If that is also blank, the server's OS default printer is used.

Because the bytes are sent from the server (the admin PC), a per-cashier value
should normally be a **network** address — `tcp://<counter-ip>:9100` — so the bill
prints at that counter rather than on a queue attached to the admin PC. A plain
queue name would resolve against the **server's** CUPS/Windows spooler. Everything
else about the receipt (paper width, logo, secondary name, footer) still comes from
the global Settings — only the destination is per-cashier.

Reprints (`/cashier/receipts`), refund slips, and warranty slips follow the same
per-cashier rule.

> Cash drawers are independent too: each cashier's till/Z-report is keyed to their
> user account, so give every cashier their **own** login.

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
2. **Admin → Settings → Printers & Labels**: set the **Label printer** field to
   that queue, and the **default label size** (default 50 × 25 mm). Leave it blank
   for the system default.

### Sticker size

The size is chosen **per print** on the Labels page — presets (50×25, 50×30, 40×30,
38×25, 100×50) or **Custom…** (width / height / gap in mm). "Default" uses the size
saved in Settings (`settings.label_width_mm` / `label_height_mm` / `label_gap_mm`).

## Logo & non-Latin shop name (printed as images)

The receipt header can carry an image **logo** and a **secondary, non-Latin shop
name** (e.g. Sinhala/Tamil) — both things the printer's built-in font can't draw.
`internal/receiptimg` rasterizes them into ESC/POS raster blocks (`GS v 0`) using
embedded **Noto Sans Sinhala/Tamil** fonts, so they print correctly **as images**
at the top of the thermal receipt (no `?`, no garbage). The logo is stored offline
in the DB as a data URI (`settings.logo_data`), so it needs no internet and keeps
the binary self-contained.

Set both in **Admin → Settings**: upload a logo and fill the **shop name (your
language)** field (`settings.shop_name_si`).

**Remaining limit — body text is built-in font only.** The receipt *body* (item
names, quantities, totals, footer) is printed with the printer's built-in
Latin/ASCII font for speed and crispness, so it is **not** rasterized: any
non-Latin character there is replaced with `?` (the `ascii()` filter in
`internal/escpos`) to avoid PC437 garbage. Only the header — logo + secondary shop
name — renders as an image. Use the HTML receipt page (`/cashier/receipt/:id`) if
you need non-Latin item names on paper. Receipt numbers, dates, and amounts are
ASCII and always print correctly.

## Troubleshooting

- **Nothing prints / `lp failed`** — check `lpstat -p` (queue enabled?),
  `lpstat -o` (stuck jobs?), and that the printer is powered/connected.
- **Garbage characters or huge feed return** — the queue is no longer raw (a driver
  got installed). Recreate it with `-m raw`.
- Inspect what was sent: a correct job is small (~1 KB in `lpstat -W completed -o`);
  a ~15–20 KB job means a PDF was sent (wrong path).
