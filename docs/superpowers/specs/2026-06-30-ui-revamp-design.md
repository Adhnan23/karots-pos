# UI/UX Revamp — Design Spec

**Date:** 2026-06-30
**Branch:** `ui-revamp` (experimental — merge if approved, otherwise discard)
**Scope:** Presentation only. **No functionality changes.** Backend services, routes,
handlers, and HTMX fragments are reused untouched. Adding/removing *admin pages or
landing pages* is allowed where it improves the information architecture; business
behavior is not.

## Problem

The UI grew incrementally and now feels cluttered and inconsistent ("mind fog"):

- **Two-level admin navigation.** Sidebar section → an intermediate *hub page of cards*
  → the actual page. Most tasks cost an extra click through a menu that adds no value.
- **Fan-out.** Reports alone is a flat list of 17; Setup is 8. Hard to scan.
- **No design system.** Buttons, tables, forms, and spacing differ page to page.
- **Not responsive.** The admin sidebar is a fixed `w-60`; admin is unusable on a phone.
  Cashier is not optimized for tablet/portrait.
- **Discoverability.** Power-user search exists (⌘K palette) but most users "don't know
  what to search" — they need to *see and recognize* where to go.

## Goals (success criteria)

1. **Consistency everywhere** — one design system; every page feels like one product.
2. **Browsable, recognizable navigation** — color-coded areas + icons so people navigate
   by sight, not by knowing what to type. Search is an accelerator, not the only path.
3. **Flatter** — remove the section→hub→page double-click; one click to a page.
4. **Touch-friendly AND keyboard-only-friendly** — first-class, both. 44px+ targets;
   full keyboard reachability + shortcuts; no mouse required.
5. **Fully responsive** — cashier on touch terminal + tablet; admin on desktop + phone.
6. **Colorful & approachable** — color as a usability/wayfinding tool, not decoration.
7. **Themeable from Settings** — saved, named themes with instant quick-switch.
8. **Plugin-proof** — plugins inherit the design system automatically.

## Non-goals

- No SPA / framework rewrite. No new JSON API. No change to business logic, money math,
  permissions, or data model **except** the small additive theming tables/fields below.
- No unrelated refactors beyond what the revamp touches.

## Stack decision

**Keep the current stack:** Templ (server-rendered) + HTMX + Alpine.js + Tailwind v3,
self-hosted vendor assets (no CDN). Same engine, new body. Rationale: reuses all
behavior (UI-not-functionality), lowest risk, fast, server-rendered, and the branch
stays cleanly discardable. A SPA rewrite was rejected as a functionality rewrite in
disguise (months of work, two codebases, behavior risk) — and responsive/touch/keyboard
goals are all achievable in Tailwind + Alpine.

## Binding design principles (apply to EVERY phase and every subagent)

These are non-negotiable, owner-stated rules for the whole revamp:

1. **Design fresh — take no reference from the existing implementation, at all.** Do not
   read, copy, or imitate the markup, classes, layout, or components of any existing page,
   shared component, layout, or plugin page. The current UI is what we are replacing; it is
   not a reference of any kind. Design from a blank slate against the design tokens,
   informed only by *what each feature needs to do*.
2. **Design from the product's full capability, not from the old screens.** The system's
   whole feature set is known (selling, inventory, purchasing, money/cashflow/lockers,
   reports/finance, setup, audit, plus the recharge & documents plugins). Design the *best*
   UI for each capability, not a reskin of how it happens to be laid out today.
3. **The new IA does NOT map 1:1 to the old pages.** One old page may split into several
   new pages; several old pages may merge into one. **Adding new pages and omitting/removing
   pages is expected and encouraged** wherever it makes the product clearer and easier.
4. **Aim for a genuinely excellent, easy UI/UX** — professional, consistent, approachable;
   anyone can use it from admin to cashier. Easy-to-use beats feature-dense layout.
5. **Behavior is preserved; only presentation/IA changes.** Business logic, money math,
   permissions, and data stay intact (except the additive theming data). When a new page
   needs server data, it reuses existing services/handlers — but never inherits old markup.

---

## 1. Design system foundations

### 1.1 Tokens (CSS variables + Tailwind theme)

All color/spacing/radius expressed as CSS variables so the whole app is re-themeable by
swapping variable values (this is what makes Settings-driven theming free).

- **Semantic surface/text tokens:** `--surface`, `--surface-2`, `--border`, `--text`,
  `--text-muted`, `--ring`.
- **Status tokens:** success / warning / danger / info.
- **Accent tokens:** `--accent`, `--accent-fg` (the active theme's brand color).
- **Area accent colors** (wayfinding — each major area owns a consistent color used on its
  nav item, page header accent, and landing tiles):
  - 🧾 Sell = emerald · 📦 Inventory = blue · 🛒 Purchasing = violet ·
    💰 Money = amber/gold · 📊 Reports = cyan · ⚙️ Setup = slate ·
    plugins each get a distinct accent (📶 Reload = pink, 🖨 Documents = teal).
- **Light + dark** are token value sets; theme + mode select which set is emitted.
- **Typography:** one clean sans (Inter, self-hosted, or system stack fallback), tight
  type scale, tabular numerals for money.
- **Touch sizing:** base control height ≥ 44px (larger in cashier "Large-touch" density),
  generous spacing.

### 1.2 Shared component library — `templates/ui/`

A new package of Templ components used on **every** page (the consistency layer). Plugins
import it too (no cycle: it lives in the templates layer, which plugins already import).

Components (initial set): `Button` (primary/secondary/ghost/danger × sizes), `Input`,
`Select`, `Textarea`, `Toggle`, `Checkbox`/`Radio`, `Card`, `SectionHeader`,
`PageHeader` (title + actions + breadcrumb), **`Table`** (responsive — collapses to
stacked cards on phone) + `EmptyState`, `Modal` / **`Sheet`** (bottom-sheet on phone),
`Badge`/`Pill`, `StatTile`, `ActionTile` (big icon nav tile), `Tabs`, `FilterBar`,
`DateRangeBar` (wraps existing `shared.RangeForm`). Existing toast/confirm Alpine hosts
are folded in unchanged.

### 1.3 Icons — embedded inline SVG

A vendored MIT-licensed icon set (Lucide-style) converted into Templ components under
`templates/ui/icons/`. Because Templ compiles into the Go binary, icons ship **inside the
binary** with zero runtime fetch, scale crisply, and recolor via `currentColor` (so they
pick up each area's accent automatically). No icon fonts, no external requests. Emoji
remain available as a fallback; SVG is the consistent default.

### 1.4 Settings-driven theming (saved themes + quick-switch)

**Model:** a **Theme** is a named bundle of appearance values.

- New additive **`themes`** table: `id`, `name`, `palette` (preset key), `mode`
  (`light`/`dark`/`auto`), `density` (`comfortable`/`compact`/`large_touch`),
  `accent` (nullable custom hex, contrast-guarded), `is_builtin`, timestamps.
  Seeded with built-in themes (the curated palettes) via a migration.
- New additive setting **`active_theme_id`** on the settings table (one migration,
  existing settings pattern).
- **Apply:** the base layout reads the active theme and emits a `<style>` block setting
  the CSS variables → entire app (admin + cashier) recolors/resizes instantly.
- **Appearance settings section** (on the Settings page): list themes as live swatches,
  set active, and create/edit/delete custom themes (add as many as you like). Curated
  presets are contrast-checked; the optional custom accent runs a contrast guard.
- **Quick-switcher** (next to the dark-mode toggle): one-tap switch of the active theme —
  "quick switch is easier." **Phase 0a scope:** the switcher lives in the **admin shell
  only**, because activation (`POST /admin/themes/:id/activate`) is an admin-gated,
  shop-level action — a cashier-role control would only 403. The **cashier shell still
  recolors automatically** via the base-layout `#theme-vars` injection; it simply has no
  switch control. A cashier-visible *read-only* current-theme indicator is a later
  decision and must not widen the activation permission.

This is the only data-model change in the revamp, and it is purely additive.

> **Phase 0a deferral (entry condition for Phase 0b/0c):** `Theme.Mode`
> (`light`/`dark`/`auto`) is stored but **not yet consumed** — the legacy `html.dark`
> toggle + its `!important` neutral overrides still own the base background/text in dark
> mode, so a theme's *own* dark neutrals are currently inert (palette/accent/area colors
> do flow through in both modes). Wiring `Theme.Mode` to drive the light/dark default and
> migrating the hardcoded `body bg-slate-100 text-slate-800` classes onto tokens is
> Phase 0b/0c work.

---

## 2. Navigation & information architecture

### 2.1 Admin — desktop

- **Color-coded sidebar** where each area **expands inline** to its pages — one click to
  the page, **no intermediate hub menu**. Active area + page highlighted in the area's
  accent color. Collapsible to an icon rail for more canvas. Sticky.
- Search/jump bar (⌘K palette) pinned at the top of the sidebar — secondary path.

### 2.2 Admin — phone (the unlock)

- **Top app bar:** menu button (opens full grouped nav drawer) + page title + search.
- **Bottom tab bar:** the 5 most-used areas as thumb-reachable colored icons +
  a **"More"** sheet for the rest.
- Tables render as tap-friendly **stacked cards**; forms get a sticky save bar.

### 2.3 Area landings become useful (not a tax)

Each area's landing page is a **colorful dashboard of big icon ActionTiles** (the area's
common actions) + relevant stat tiles — browsable and recognizable for users who don't
know what to search. This *replaces* the old plain card-menu hub: you are no longer forced
through a menu (the sidebar/bottom-bar reach pages directly), but the landing is a genuinely
useful at-a-glance screen when you do land on it.

### 2.4 Reports (17 → grouped)

Group into ~4 recognizable colored buckets on the Reports hub, each with its reports as
tiles + a quick filter, instead of a flat list of 17:

- **Sales & Trends** (sales, sales-trend, profit-by-category, returns)
- **Money & P&L** (finance/P&L, tax, cash-register, damage/recovery)
- **Inventory & Stock** (inventory valuation, batches/expiry, expiring, low-stock)
- **Customers & Suppliers** (customer dues, supplier dues, purchases, suppliers)

### 2.5 Cashier

POS-first and touch-first. The topbar tabs adapt to a **bottom bar** on tablet/portrait.
All keyboard shortcuts kept and made **visible** on screen. Plugin cashier tabs and
quick-action tabs flow through the same responsive nav components.

---

## 3. Page patterns (every page conforms to one of five)

1. **List page** — `PageHeader` (title + primary action) → `FilterBar` → responsive
   `Table` (→ stacked cards on phone) → `EmptyState`. *(products, sales, customers,
   suppliers, expenses, stock, purchases, …)*
2. **Form / detail page** — `PageHeader` → form in `Card` sections → sticky Save/Cancel
   bar (esp. mobile). *(product form, customer, supplier, settings)*
3. **Dashboard / area landing** — `StatTile`s + `ActionTile`s, role-aware. *(admin home,
   each area landing)*
4. **Report page** — `DateRangeBar` + filters → summary `StatTile`s → chart(s) (existing
   SVG chart kit) → `Table` → CSV export. *(the 17 reports)*
5. **Cashier workspace** — full-height touch grid + cart. *(POS, returns, credit, …)*

## 4. Touch / keyboard / accessibility rules

- **Touch:** ≥44px targets (bigger in Large-touch density); primary actions within thumb
  reach on phone; bottom-sheets instead of center modals on phone.
- **Keyboard:** everything tab-reachable; visible focus rings; Esc closes; Enter submits;
  ⌘K palette everywhere; existing POS shortcuts (F2/F3/F9/F10/Esc…) kept and shown.
- **A11y:** AA contrast (presets enforce it; custom accent contrast-guarded); never rely
  on color alone (always icon + label); `inputmode="numeric"` for money/phone fields;
  `prefers-reduced-motion` respected.

## 5. Rollout sequence (on the branch; always compiles + demoable)

- **Phase 0 — Foundations:** tokens + Tailwind config, theming tables/migrations +
  Settings Appearance section + quick-switcher, `templates/ui/` components, embedded SVG
  icons, the two responsive shells + new nav (sidebar/drawer/bottom-bar/landings). The app
  immediately feels new even before page bodies are reworked.
- **Phase 1 — Prove the patterns on the busiest pages:** Dashboard, Products list+form,
  Sales list, Cashier POS.
- **Phase 2 — Remaining admin list/form pages:** customers, suppliers, stock/stock-take,
  purchases/returns, expenses, lockers/cashflow/receipts.
- **Phase 3 — Reports (regroup + reskin) + Setup pages.**
- **Phase 4 — Remaining cashier pages** (returns, damage, credit, receipts, warranty,
  labels) **+ plugin pages** (recharge, documents) conformed via shared components.
- **Phase 5 — Polish:** a11y/keyboard audit, mobile QA across pages, remove dead hub
  pages, finalize themes.

Each phase compiles and runs; we can **stop, merge, or discard after any phase.**

## Risks & mitigations

- **Scope (≈48 pages).** Mitigated by the shared component library (most pages are just
  re-composition of the same components) and phased rollout with stop points.
- **Theming readability.** Curated, contrast-checked presets; custom accent runs a
  contrast guard; never color-alone.
- **Plugin drift.** Plugins import `templates/ui/`, so they inherit the system; the
  existing plugin UI hooks are preserved.
- **`make css` dependency.** New utility classes require `make css`; `tailwind.css`
  stays unstaged per project convention. `_templ.go` are generated + gitignored.

## Verification

- App builds (`make templ && make css && go build ./...`), `go vet`, `go test` green
  after each phase.
- Manual responsive QA at phone / tablet / desktop widths for each converted page.
- Keyboard-only walkthrough (tab/enter/esc/⌘K) of converted pages.
- Theme quick-switch recolors admin + cashier without reload artifacts.
- Plugin pages (recharge, documents) render correctly under the new system when enabled.
