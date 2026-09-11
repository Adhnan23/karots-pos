# Receipt Styles Rework — Design

Date: 2026-09-11
Branch: `feature/receipt-styles`

## Problem

The receipt is the one artifact a customer physically holds and compares
between shops. The white-label appearance system (merged `9351423`) added a
system-user-locked `receipt_style` setting with four profiles
(classic/compact/bold/minimal), but they differ only by subtle Header/Footer
knobs (a rule character, double-height name, a thank-you line, spacing). Two
shops on different styles still look "a little bit the same." The owner wants
**totally different kinds of receipt** — distinct at a glance — and is happy to
invest more here than in the web UI (where colour skins are enough).

## Constraints

- **Two independent render targets, both must carry the style:**
  - **Thermal (ESC/POS):** `internal/escpos/` — `Header`/`Footer`/`divider`/
    `bigLine`/`Title`/`Document`. Only Header/Footer vary today.
  - **Web view (HTML):** the `.receipt` + `.r-*` DOM in
    `templates/pages/cashier/receipt.templ` and the shared
    `templates/shared/thermal.templ` (`ThermalReceipt`/`ThermalHeader`/
    `ThermalFooter`), styled by CSS in `static/css/app.css`.
- **ASCII-safe only.** Use `+ - = | .` and reverse-video (`GS B`, well
  supported). No box-drawing (╔═╗) or block (▓) glyphs — cheap thermal heads
  render them as garbage.
- **Do not touch barcode/label printing.** Receipts only.
- Style stays **system-user-locked, one binary, per shop** — the existing
  `settings.receipt_style` mechanism is reused unchanged (no new setting, no
  migration). The four keys are redefined; default stays `classic`.
- Every core AND plugin receipt (sale, money/cashflow, recharge slip, repairs
  slip, credit/cash/warranty views) inherits the look for free, because they
  all route through the shared primitives.

## The four looks

A shop picks one; the point is any two shops on different styles look nothing
alike. Mockups at 58mm / 32 columns.

- **classic** — centered header, dashed body dividers, `*** TITLE ***`,
  double-height TOTAL between `=` rules. Formal, the refined current look.
- **modern** — left-aligned header, NO rule lines (blank-line separation),
  lowercase meta labels, bold TOTAL with a single `=` underline. Airy.
- **boxed** — header framed with `+==+` corners and `|` sides; dotted
  (`.`-leader) separators; reverse-video TOTAL bar. Branded/premium.
- **compact** — dense, tiny, dotted leaders join item→price, single `-` rule
  before TOTAL, no thank-you. Corner-shop chit.

## Design

### 1. `ReceiptStyle` becomes a real profile

Extend the struct in `internal/escpos/style.go` (keys mirror
`settings.ReceiptStyles`):

```go
type ReceiptStyle struct {
    Key        string
    HeaderRule string // full-width char under header/above footer ("" = none)
    Body       string // body separator: "dash" | "blank" | "dots" | "compact"
    HeaderLeft bool   // left-align the header block (else centered)
    Framed     bool   // box the header block with +==+ / | sides
    NameDouble bool   // shop name double-width+height (else double-height only)
    TotalMode  string // "double" | "reverse" | "plain"
    TitleDeco  string // fmt for Title, e.g. "*** %s ***" | "-- %s --" | "%s"
    ThankYou   bool
    Breathing  bool
}
```

`StyleFor(cfg)` returns one of four literals; unknown → classic. The web side
reads the same four keys from CSS (no Go profile needed there).

### 2. Thermal: thread the body separator

The body skeleton (the repeated separators between meta / items / totals) is
the biggest differentiator and is currently a hard-coded `-`.

- Unexported `divider(b, w)` → `divider(b, w, style)`; renders per `style.Body`:
  `dash` = `strings.Repeat("-", w)`, `blank` = an empty line, `dots` =
  `strings.Repeat(".", w)`, `compact` = a short `-` run. (~20 internal calls,
  mechanical.)
- Exported `escpos.Divider(b, w)` → **`escpos.Divider(b, cfg, w)`** so external
  slip builders pass the style. **11 callers** update (all already hold `cfg`):
  `plugins/repairs/repairs.go` ×5, `plugins/recharge/slip.go` ×3,
  `internal/web/admin_money_receipts.go` ×3.
- `Header`: honour `HeaderLeft` (skip the `center()` padding, print flush left)
  and `Framed` (wrap the name/address block in `+==+` top/bottom and `| … |`
  sides, ASCII). `NameDouble` unchanged.
- `bigLine`/TOTAL: `TotalMode` — `double` (current), `reverse` (`GS B 1` white
  -on-black bar, `GS B 0` after), `plain` (bold single line + `=` underline).
- `Title`: decorate via `TitleDeco`.
- `plainText`/`SamplePreview` unchanged in shape — they strip the same control
  bytes, so the appearance-panel preview stays a true render of the output
  (add `GS B` to the stripped set so the reverse bar previews as plain text).

### 3. Web: mirror the profile in CSS

- Add `ReceiptStyle string` to `shared.ThermalData`; stamp
  `data-rstyle={style}` on the `.receipt` container in **both**
  `thermal.templ` (`ThermalReceipt`) and `receipt.templ`. The style comes from
  `d.Settings.ReceiptStyle` (already on the page data).
- Pass the style into `ThermalHeader`/`ThermalFooter` so the framed/left
  variants can adjust markup where CSS alone can't (mainly the frame).
- Per-style CSS in `app.css`, keyed on `.receipt[data-rstyle="…"]`:
  - `modern`: `text-align:left`; `.r-hr{border:0;height:.4rem}` (blank gap);
    lowercase meta.
  - `boxed`: `.receipt{border:2px solid #000;padding:...}`; `.r-hr{border-top-
    style:dotted}`; `.r-total{background:#000;color:#fff;padding:2px 6px}`.
  - `compact`: smaller base font, tight margins, dotted `.r-hr`, leader dots on
    `.r-row` via a flex `::before` filler.
  - `classic`: current defaults (dashed `.r-hr`, centered, double total).
  Dark-mode: the reverse/boxed backgrounds use explicit `#000/#fff`, so guard
  with the existing dark-accent conventions in app.css if needed.

### Testing

- Extend `internal/escpos/style_test.go`: assert the four profiles are mutually
  distinct in the bytes that matter (body separator char, header alignment,
  total-mode control bytes, framing), and that `StyleFor` falls back to classic.
- Live: appearance-panel preview per style (already renders real output) + one
  real sale receipt printed to the emulator per style; one web receipt view per
  style.

## Files touched

- `internal/escpos/style.go` — profile struct + 4 profiles + preview strip.
- `internal/escpos/escpos.go` — `divider`, `Divider`, `Header`, `bigLine`,
  `Title`.
- `internal/escpos/style_test.go` — distinctness tests.
- `plugins/repairs/repairs.go`, `plugins/recharge/slip.go`,
  `internal/web/admin_money_receipts.go` — `Divider` signature (11 calls).
- `templates/shared/thermal.templ`, `templates/pages/cashier/receipt.templ` —
  `data-rstyle` + style pass-through.
- `static/css/app.css` — per-style receipt CSS; `make css` to recompile.

## Out of scope

- Barcode/label printing (unchanged, per owner).
- Web UI skins/colours/density (already shipped; not revisited).
- Layout/shell variants (Stage 3, dropped by owner).
- New settings or migrations (reuses `receipt_style`).
