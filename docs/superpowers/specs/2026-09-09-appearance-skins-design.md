# Appearance Skins + Receipt Styles — Design

**Date:** 2026-09-09
**Branch:** `feature/appearance-receipt-styles`
**Status:** approved design, pre-implementation

## Problem

Each client wants the POS to look visibly different from other shops' installs —
different web UI (colour, density, card shapes, layout) *and* different printed
receipts — without forking the codebase. The choice must be **locked**: only the
**system user** (vendor, via the hidden system-admin + rotating support PIN) can
set it; not even the shop admin. Set once at onboard, re-skinnable later over the
support login. One binary for every shop.

## Terminology (important — avoids a name clash)

- **skin** — the vendor-locked brand identity: colour family, font, corner radius,
  density, card style, layout variant, plus a light **and** dark palette. NEW.
- **theme** — the EXISTING per-user light/dark toggle (`localStorage['theme']` +
  `.dark` class on `<html>`, `darkMode:"class"`). Unchanged. Dark/light stays a
  per-user switch *within* the locked skin.

Do not conflate the two. Settings column is `skin`, attribute is `data-skin`.

## Architecture: token + attribute layer (no per-page template edits)

Everything keys off attributes stamped on `<html>` (only `templates/layouts/base.templ`
has the `<html>` tag — the single integration point) plus the existing `.dark` class:

```html
<html lang="en" class="h-full dark" data-skin="teal" data-density="compact" data-layout="sidebar">
```

A new stylesheet layer (`static/css/theme.css`, or an `@layer` block in the
existing `tailwind.input.css`) defines what those select. Because the accent is a
single family — `indigo`, ~600 uses across 132 files — one `@layer` remap covers
the whole app with **zero template edits**:

- **Colour tokens** — `--brand-50…900` + neutrals (`--surface`, `--surface-2`,
  `--text`, `--muted`, `--border`). An `@layer utilities` block maps the ~15
  heavily-used indigo utilities to the tokens: `.bg-indigo-600{background:var(--brand-600)}`,
  `.text-indigo-600{color:var(--brand-600)}`, `.ring-indigo-500{--tw-ring-color:var(--brand-500)}`,
  etc. (`ponytail:` deliberate override of stock utilities; formalise as a Tailwind
  `brand` colour + class sweep only if a shop needs per-shade control the override
  can't give.)
- **Dark/light** — each skin defines both palettes; token values are redefined
  under `:root[data-skin=X]` (light) and `.dark[data-skin=X]` / `:root[data-skin=X].dark`
  (dark). The existing 🌙 toggle flips `.dark`; nothing about the toggle changes.
- **Density** — `data-density` (comfortable|compact) scales a spacing token that a
  handful of common container paddings read.
- **Cards** — `--radius`, `--shadow`, `--border-color` tokens restyle card/button
  shapes via the same override layer.
- **Layout** — `data-layout` drives structural CSS (grid/flex direction, nav
  placement) against the shell's existing containers. STAGE 2 (see Staging).
- **Logo** — reuses `settings.logo_data` (self-contained, offline-safe).

New skins are added by appending a token block to `theme.css` + registering the
skin key — no Go changes.

## Storage + the system-user lock

New columns on the `settings` singleton (migration, `DEFAULT` = the seed):

| column          | default        | values                          |
|-----------------|----------------|---------------------------------|
| `skin`          | `'default'`    | skin key (matches theme.css)    |
| `density`       | `'comfortable'`| comfortable \| compact          |
| `layout`        | `'default'`    | layout key (stage 2)            |
| `receipt_style` | `'classic'`    | classic \| compact \| bold \| minimal |

- Read into the `settings.Settings` struct; `base.templ` stamps the `data-*`
  attributes from them (threaded through the layout's view data).
- **Changed only** through a **system-user-gated** panel/route. Reuse the existing
  `is_system` guard (auth model `IsSystem`; the `middleware/flags.go` support-gate
  pattern). The panel is invisible to and rejected for the shop admin. A new
  `RequireSystemUser` middleware (or reuse of the support gate) protects
  `GET/POST /system/appearance`.
- The normal admin Settings page does NOT expose these fields.

## Onboard seeding

The bootstrapper produces only a binary + `.env.sample` (never touches the shop
DB). So seeding is: the migration `DEFAULT`s give a working skin on first run, and
the vendor sets the shop's real skin at onboard via the system-user panel over the
support login. No bootstrapper change required. (Optional later: a
`POS_DEFAULT_SKIN` env the app applies to the settings row on first run — not in
this scope.)

## Receipts (ReceiptStyle)

`escpos.Header(b, cfg, opts)` and `escpos.Footer(b, cfg)` already receive the full
settings struct, so `receipt_style` reaches exactly where branding is drawn. A
`ReceiptStyle` profile (derived from `cfg.ReceiptStyle`) varies: header/banner
treatment, divider glyph, line spacing, logo placement, name emphasis. Core AND
every plugin receipt call these primitives, so all inherit the style with no
per-plugin work. ~4 presets: classic / compact / bold / minimal.

## Staging (one branch, verifiable steps)

1. **Foundation core** — token model + `theme.css` colour/dark/light remap +
   density + card tokens; `settings` columns + migration; `base.templ` attribute
   stamping; system-user appearance panel (skin + density + receipt_style pickers)
   with the lock; 2–3 shipped skins (e.g. Indigo/default, Teal, Slate) each with
   light+dark. Verify: two skins look clearly different, dark/light works in each,
   admin cannot reach the panel.
2. **Receipt styles** — `ReceiptStyle` profiles in `escpos`; wire `cfg.ReceiptStyle`;
   verify each preset renders on a core sale receipt AND a plugin receipt.
3. **Layout variants** — `data-layout` structural CSS + minimal shell hooks; add
   layout options to the panel. Higher risk; done last on the proven foundation.

## Testing

- Unit: `ReceiptStyle` selection + escpos output per style (byte-level, like
  existing escpos tests); skin-key validation (unknown key → default, never blank).
- Guard: the appearance route rejects a non-system user (admin) — a middleware test.
- Manual/live: each skin in light + dark across an admin page and a cashier page
  and one plugin page; each receipt style printed via the emulator.
- Inert: core still builds with plugins stripped (appearance is core, not a plugin,
  but the CSS-var remap must not break plugin pages — verify a plugin page reskins).

## Out of scope (explicit)

- **UI componentization refactor** — tracked separately, not bundled (the token
  layer reskins without it).
- Per-shop custom colour picker (only curated skins).
- Build-time baking (rejected: one binary + system-user lock chosen instead).

## Risks / ceilings

- `@layer` overriding stock utilities with the token vars is a deliberate shortcut
  (`ponytail:`). Ceiling: can't vary a shade the override doesn't target; upgrade
  path is a Tailwind `brand` colour + class sweep. Fine for curated skins.
- Layout variation is the one axis that leans on shell structure; isolated to
  stage 3 so the colour/density/card/receipt core ships proven first.
- `safelist`/`content` in `tailwind.config.js` must keep the remapped utilities;
  new token classes added to the input CSS are compiled at `make css` (commit the
  built `tailwind.css`, per project rule).
