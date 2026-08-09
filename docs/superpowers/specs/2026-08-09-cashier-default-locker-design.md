# Cashier default locker + untracked-cash toggle

Date: 2026-08-09 · Status: approved design, pre-implementation

## Problem

Every till money-move (Open, Deposit, Withdraw, Close) offers a locker picker whose
**first / default option is "untracked"** ("— not into a locker —", etc.). Choosing
it sends the money leg to cashflow **External** — the cash leaves (or enters) the
drawer with **no locker tracking**. Cashiers withdraw/close to the untracked default
by habit, so the cash trail is silently lost. The owner wants to (a) make a real
locker the default and (b) be able to forbid untracked cash movements — without
bricking the till.

## Decisions (approved with the owner)

- **Scope: all four moves** get the default-locker pre-select. Enforcement of the
  "no untracked" rule applies to **cash-OUT only** (Withdraw + Close).
- **One shared default locker + one toggle** (not separate in/out) — a single "main
  cash box" that is the default *source* for Open/Deposit and default *destination*
  for Withdraw/Close.
- **Open + Deposit are never blocked.** You cannot stop physical cash entering, and
  blocking Open would brick the till (same class of bug as the `default_sale_type`
  landmine). For these two the untracked option always remains available; the
  default locker only *pre-selects* for convenience.
- **Placement:** the two controls live on the **Settings page** (policy lives with
  the other cashier policies); the Lockers page shows a read-only "default" badge.
- **Setup checklist:** reuse the existing "Create a cash safe" step
  (`internal/web/setup.go:55`) — mark it done only when a **default locker is set**
  (which implies at least one active locker exists), and update its label/hint to
  say "and set it as the default."

## Data model

New migration (`migrations/00XX_default_locker.sql`), purely additive; defaults =
today's behaviour, so the feature is **inert until the owner opts in**:

```sql
-- +goose Up
ALTER TABLE settings ADD COLUMN allow_untracked_cash BOOLEAN NOT NULL DEFAULT true;
ALTER TABLE settings ADD COLUMN default_locker_id BIGINT
  REFERENCES lockers (id) ON DELETE SET NULL;
-- +goose Down
ALTER TABLE settings DROP COLUMN default_locker_id;
ALTER TABLE settings DROP COLUMN allow_untracked_cash;
```

- `allow_untracked_cash = true` default → untracked option still offered everywhere,
  so day-one behaviour is byte-identical.
- `default_locker_id` nullable; `ON DELETE SET NULL` so deleting the locker clears
  the default cleanly. A *disabled* locker chosen as default is treated as "no
  usable default" (falls back like an unset default).

## Behaviour

`default_locker_id` and `allow_untracked_cash` are threaded to the till the same way
as `AskToPrint` / `Symbol` — via `PosData` and the `pos(...)` x-data constructor
(`templates/pages/cashier/pos.templ`). The four pickers already list only the
cashier's **accessible** lockers (`/cashier/lockers`, filtered by `cashier_access`).

**Per-move pre-select (all four):**
1. `default_locker_id` **if it is in this cashier's accessible list** → pre-select it.
2. else if untracked is allowed → pre-select the blank (untracked) option.
3. else → pre-select the first accessible locker.

**Untracked option visibility:** the blank "untracked" option is shown when
`allow_untracked_cash` is true. When false it is hidden from all four pickers.

**Enforcement (cash-OUT only — Withdraw & Close):**
- When `allow_untracked_cash` is false, an untracked (locker-less) withdraw/close is
  **rejected server-side** (defence in depth, not JS-only).
- If the cashier also has **no accessible locker**, the till surfaces a clear
  "No locker set up — ask an admin" message instead of a silently disabled button.

**Cash-IN (Open & Deposit):** never blocked. Untracked always permitted; the default
locker only pre-selects. Open therefore can never brick.

## Server-side

- `cashregister` Withdraw and Close (register-close) handlers/services: when
  `allow_untracked_cash` is false and the chosen counter/destination locker id is 0,
  return `apperr.Validation("untracked withdrawals are disabled — choose a locker")`.
- Open (PayIn/Open) and Deposit paths: unchanged (untracked allowed).
- The handlers read the flag from `settings` (already injected as `h.s.settings`).

## UI

- **Settings page** — new "Cashier cash movements" group:
  `[x] Allow untracked cash movements` (checkbox) and
  `Default locker: [dropdown of ACTIVE lockers]` (disabled + hint "create a locker
  first" when none exist). Wired through `SettingsForm` + the update SQL.
- **Lockers page** — read-only "Default" badge on whichever locker is
  `default_locker_id`.
- **Till** (`app.js`) — pre-select logic for the four pickers; hide the untracked
  option when the flag is off; guard the Withdraw & Close buttons (message when no
  accessible locker + untracked off).
- **Setup checklist** — existing "Create a cash safe" step becomes done when
  `default_locker_id` is set; label/hint updated.

## Files touched

- `migrations/00XX_default_locker.sql` (new)
- `internal/features/settings/settings.go` (struct, form, update SQL)
- `internal/features/cashregister/cashregister.go` (Withdraw/Close server guard)
- `templates/pages/admin/settings.templ` (the two controls)
- `templates/pages/admin/lockers.templ` (default badge)
- `templates/pages/cashier/pos.templ` (`PosData` + `pos()` args)
- `static/js/app.js` (pre-select + visibility + button guard)
- `internal/web/setup.go` (checklist step done-condition + copy)
- `static/css/tailwind.css` if any new utility classes are introduced

## Testing

- DB-guarded test: with `allow_untracked_cash=false`, a locker-less Withdraw is
  rejected; with a locker it succeeds and the money lands in that locker. Close
  mirrors this. Open with no locker + flag off still succeeds (never bricks).
- Inertness: with defaults (`allow_untracked_cash=true`, no default locker) every
  move behaves exactly as today.

## Out of scope

Separate in/out default lockers or per-cashier defaults; changing what External
means; forbidding untracked cash-IN.
