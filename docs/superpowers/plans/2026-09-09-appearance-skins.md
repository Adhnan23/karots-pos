# Appearance Skins + Receipt Styles Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A system-user-locked appearance system — a "skin" (colour/font/radius/density/card style, with light+dark palettes) and a receipt style — that reskins core + all plugins with no per-page template edits.

**Architecture:** CSS design-token + `<html>` data-attribute layer (`data-skin`/`data-density`), stamped server-side by `base.templ` reading a request-context value a middleware populates from the `settings` singleton. An `@layer` remaps the single accent family (`indigo`) to `--brand-*` tokens so all 132 files reskin at once. Receipts vary via a `ReceiptStyle` derived from `settings.receipt_style`, threaded through the already-settings-aware `escpos.Header/Footer`. The appearance is changed only through a system-user-gated panel.

**Tech Stack:** Go, templ, HTMX/Alpine, Tailwind v3 (class dark-mode), goose migrations, ESC/POS (`internal/escpos`), sqlx.

**Spec:** `docs/superpowers/specs/2026-09-09-appearance-skins-design.md`

## Global Constraints

- Terminology: **skin** = vendor-locked brand identity (NEW); **theme** = the existing per-user light/dark toggle (`localStorage['theme']` + `.dark` class). Never conflate; column is `skin`, attribute `data-skin`.
- Appearance is CORE, not a plugin. It must not break plugin pages, and core must still build with plugins stripped.
- Only the **system user** (`users.is_system = true`) may change appearance. The normal admin Settings page must NOT expose these fields.
- Unknown/blank skin, density, or receipt_style must resolve to a safe default, never render blank.
- Run `make css` after adding token classes; commit the built `static/css/tailwind.css` (go:embed).
- Migrations: next core number is **0068**.
- Attribution on every commit:
  `Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>`
  `Claude-Session: https://claude.ai/code/session_01EdqF8cvGdw27z5jmRBXTTg`

---

## STAGE 1 — Foundation (skin: colour + dark/light + density + cards, system-user lock)

### Task 1: settings columns + struct + validation

**Files:**
- Create: `migrations/0068_appearance.sql`
- Modify: `internal/features/settings/settings.go` (Settings struct + UpdateInput + Update SQL, or a dedicated appearance update)
- Test: `internal/features/settings/appearance_test.go`

**Interfaces:**
- Produces: `settings.Settings{ Skin string, Density string, ReceiptStyle string }` (db `skin`,`density`,`receipt_style`); `settings.ValidSkin(s) string`, `settings.ValidDensity(s) string`, `settings.ValidReceiptStyle(s) string` (each returns the input if known, else the default); `(*settings.Service).SetAppearance(ctx, skin, density, receiptStyle string) error`.

- [ ] **Step 1: migration**

```sql
-- +goose Up
ALTER TABLE settings ADD COLUMN skin           TEXT NOT NULL DEFAULT 'default';
ALTER TABLE settings ADD COLUMN density        TEXT NOT NULL DEFAULT 'comfortable';
ALTER TABLE settings ADD COLUMN receipt_style  TEXT NOT NULL DEFAULT 'classic';
-- +goose Down
ALTER TABLE settings DROP COLUMN receipt_style;
ALTER TABLE settings DROP COLUMN density;
ALTER TABLE settings DROP COLUMN skin;
```

- [ ] **Step 2: add fields to the Settings struct** (mirror existing `db:`/`json:` tag style) and validators + a registry of known keys:

```go
var (
	Skins         = []string{"default", "teal", "slate"}
	Densities     = []string{"comfortable", "compact"}
	ReceiptStyles = []string{"classic", "compact", "bold", "minimal"}
)

func oneOf(v string, allowed []string, def string) string {
	for _, a := range allowed { if v == a { return v } }
	return def
}
func ValidSkin(v string) string         { return oneOf(v, Skins, "default") }
func ValidDensity(v string) string      { return oneOf(v, Densities, "comfortable") }
func ValidReceiptStyle(v string) string { return oneOf(v, ReceiptStyles, "classic") }
```

- [ ] **Step 3: SetAppearance service method** (validates then writes the three columns on the id=1 row).

- [ ] **Step 4: failing test**

```go
func TestValidSkinFallsBack(t *testing.T) {
	if got := ValidSkin("nope"); got != "default" { t.Fatalf("want default, got %q", got) }
	if got := ValidSkin("teal");  got != "teal"    { t.Fatalf("want teal, got %q", got) }
	if got := ValidReceiptStyle("");  got != "classic" { t.Fatalf("want classic, got %q", got) }
}
```

- [ ] **Step 5: run** `go test ./internal/features/settings/ -run TestValidSkin -v` → PASS
- [ ] **Step 6: commit** `feat(appearance): settings columns + skin/density/receipt-style validators (migr 0068)`

### Task 2: appearance in request context + base.templ stamping

**Files:**
- Modify: `internal/middleware/` (new `appearance.go`: middleware + ctx accessor)
- Modify: `templates/layouts/base.templ` (stamp `data-skin`/`data-density` on `<html>`)
- Modify: wherever core middleware is chained (register the middleware for full-page routes)
- Test: `internal/middleware/appearance_test.go`

**Interfaces:**
- Consumes: `settings.Service.Get`, `settings.ValidSkin/ValidDensity`.
- Produces: `middleware.WithAppearance(settingsSvc)` echo.MiddlewareFunc; `middleware.SkinCtx(ctx) (skin, density string)` — returns `"default","comfortable"` when unset (safe for any request).

- [ ] **Step 1: middleware** loads the settings singleton once per request, stores validated `skin`/`density` in the echo context + request context; `SkinCtx(ctx)` reads them with safe defaults.
- [ ] **Step 2: base.templ** — add the attributes to the `<html>` tag using the ctx accessor, mirroring the existing `middleware.CanExitKioskCtx(ctx)` usage:

```
<html lang="en" class="h-full" data-skin={ skinOf(ctx) } data-density={ densityOf(ctx) }>
```
(where `skinOf`/`densityOf` wrap `middleware.SkinCtx` — templ attribute expressions need a string.)

- [ ] **Step 3: failing test** — `SkinCtx` on an empty context returns `("default","comfortable")`; on a context seeded with `"teal","compact"` returns those.
- [ ] **Step 4: run** the middleware test → PASS
- [ ] **Step 5:** `templ generate ./templates/layouts/` && `go build ./...` → clean
- [ ] **Step 6: commit** `feat(appearance): stamp data-skin/data-density on <html> via request context`

### Task 3: the token CSS layer + shipped skins

**Files:**
- Create: `static/css/theme.css` (linked from base.templ head) OR append an `@layer` block to `static/css/tailwind.input.css`
- Modify: `templates/layouts/base.templ` head (link theme.css if separate file)
- Modify: `tailwind.config.js` (ensure remapped utilities stay in `safelist` if referenced only via tokens)

**Interfaces:** none (pure CSS keyed off `data-skin`/`data-density` + `.dark`).

- [ ] **Step 1:** define default (indigo) light tokens on `:root` — `--brand-50..900`, `--surface`, `--surface-2`, `--text`, `--muted`, `--border`, `--radius`, `--shadow`, `--space` — and dark overrides under `.dark`.
- [ ] **Step 2:** `@layer utilities` remap of the accent utilities to tokens (from the spec's list: `bg-indigo-600`, `text-indigo-600/700/500`, `bg-indigo-50/100/500/700`, `ring-indigo-300/400/500`, `border-indigo-600`). Use the token vars so a skin swap re-colours everything.
- [ ] **Step 3:** add `[data-skin=teal]` and `[data-skin=slate]` blocks (light + dark) overriding `--brand-*` and neutrals; add `[data-density=compact]` reducing `--space` and common paddings.
- [ ] **Step 4:** card tokens — remap the common card/button radius/shadow/border to `--radius`/`--shadow`/`--border`.
- [ ] **Step 5:** `make css`; build; restart; **verify live** (Playwright): switch `data-skin` on `<html>` across default/teal/slate in light AND dark — accent, cards, density change on an admin page, a cashier page, and one plugin page; no layout break.
- [ ] **Step 6: commit** `feat(appearance): token CSS layer + default/teal/slate skins (light+dark)` (include built tailwind.css)

### Task 4: system-user-gated appearance panel

**Files:**
- Modify: `internal/middleware/` (add `RequireSystemUser`, or reuse the support gate in `flags.go`)
- Create: `internal/web/system_appearance.go` (GET panel + POST save) + route registration
- Create/Modify: a templ page for the panel (skin + density + receipt_style pickers, with a live preview note)
- Test: `internal/web/system_appearance_test.go` (guard: non-system user → 403/404)

**Interfaces:**
- Consumes: `settings.Service.SetAppearance`, `settings.Skins/Densities/ReceiptStyles`, `auth` `IsSystem`.
- Produces: routes `GET /system/appearance`, `POST /system/appearance`.

- [ ] **Step 1:** `RequireSystemUser` middleware — 404 (not 403, to stay hidden) for any user with `is_system=false`, mirroring the flags.go support-gate style.
- [ ] **Step 2: failing test** — POST `/system/appearance` as an admin (non-system) is rejected; as system user it writes the columns.
- [ ] **Step 3:** handler: GET renders the picker (current values selected); POST validates via the settings validators + `SetAppearance`, then reloads.
- [ ] **Step 4:** panel templ — three selects (skin/density/receipt_style) + a Save; small note that this is system-only and applies shop-wide.
- [ ] **Step 5:** run guard test → PASS; `templ generate`; build.
- [ ] **Step 6:** verify live — as system user change skin→teal, density→compact; confirm the whole UI reskins; confirm the route 404s for the shop admin.
- [ ] **Step 7: commit** `feat(appearance): system-user-only appearance panel (skin/density/receipt-style)`

---

## STAGE 2 — Receipt styles

### Task 5: ReceiptStyle profiles in escpos

**Files:**
- Create: `internal/escpos/style.go` (`ReceiptStyle` + `StyleFor(cfg settings.Settings) ReceiptStyle`)
- Modify: `internal/escpos/escpos.go` (`Header`/`Footer`/divider honour the style)
- Test: `internal/escpos/style_test.go`

**Interfaces:**
- Consumes: `settings.Settings.ReceiptStyle`.
- Produces: `escpos.StyleFor(cfg) ReceiptStyle{ DividerChar byte, HeaderEmphasis bool, LogoOnTop bool, LineSpacing int, Banner string }` (fields as the presets need); `Header`/`Footer` read it from the passed `cfg`.

- [ ] **Step 1: failing test** — `StyleFor` returns distinct `DividerChar`/`HeaderEmphasis` for `classic` vs `compact` vs `bold` vs `minimal`, and `classic` for an unknown value.
- [ ] **Step 2:** implement `ReceiptStyle` + `StyleFor` (validated via `settings.ValidReceiptStyle`).
- [ ] **Step 3:** thread the style into `Header`/`Footer`/divider — they already receive `cfg`, so derive `StyleFor(cfg)` internally; vary banner emphasis, divider glyph, spacing, logo placement.
- [ ] **Step 4: byte-level test** — `Header` output for `bold` contains the emphasis bytes and `minimal` omits the divider, mirroring existing escpos tests.
- [ ] **Step 5:** run `go test ./internal/escpos/ -v` → PASS
- [ ] **Step 6:** verify live via the printer emulator — a core sale receipt AND a plugin (repairs/recharge) receipt render each style differently.
- [ ] **Step 7: commit** `feat(appearance): receipt-style profiles honoured by shared escpos header/footer`

---

## STAGE 3 — Layout variants (separate plan)

`data-layout` structural CSS + minimal shell hooks (`nav` placement, page grid). Higher risk; gets its own plan (`docs/superpowers/plans/…-appearance-layouts.md`) once Stages 1–2 are proven and merged-ready. Out of scope for this plan.

## Self-Review notes

- Spec coverage: colour/dark-light (Task 3), density (Task 3), cards (Task 3), storage+lock (Tasks 1,4), onboard seed = migration DEFAULT (Task 1), receipts (Task 5), layout (Stage 3, deferred). ✓
- Type consistency: `Skin/Density/ReceiptStyle` fields + validators defined in Task 1 and consumed by name in Tasks 2/4/5. `SkinCtx` defined Task 2, used in base.templ. `StyleFor` defined Task 5.
- The one integration risk to watch: registering `WithAppearance` only on full-page routes (fragments/htmx don't render `base.templ`), and not adding a per-request settings read to hot fragment endpoints.
