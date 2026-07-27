# Physical cash-drawer kick (ESC/POS) — design

**Date:** 2026-07-27
**Status:** approved (behaviour decisions locked with owner)

## Problem

A shop's cash drawer is not wired to the computer — it plugs into the **thermal
receipt printer** with an RJ11/RJ12 cable and the printer pops it open on receipt
of an ESC/POS "drawer kick" pulse. The POS already speaks ESC/POS to that printer
(init, text, cut, raster logo) but **never sends the kick**, so the physical drawer
never opens on its own. A shipping client needs it to.

## Principle (the key design decision)

**Kick on the till *cash event*, never on the paper.**

Keying the kick off "a receipt printed" is impossible to do correctly: a fresh
sale and a historical reprint hit the *same* print endpoint, and with "Ask to
print" on, a fresh receipt's first print also goes through the reprint path. So we
kick when money actually enters/leaves the physical drawer (post-commit), which:

- excludes historical reprints/views **by construction**;
- works identically whether "Ask to print" is on or off;
- opens the drawer at the moment the cashier needs it (to make change), not when
  the paper happens to feed.

## Behaviour decisions (locked with owner)

1. **Reprints / views never kick** — only live money events.
2. **Enablement:** a Settings toggle `open_cash_drawer`, **default off** (this is a
   shared codebase; shops without a drawer are unaffected). Plus a drawer **pin**
   option (pin 2 / pin 5) since a minority of drawers are wired to pin 5.
3. **Card/credit-only sales do not kick** — cash-touching events only. A mixed
   sale that took *some* cash does kick.

## Kick triggers (all post-commit, best-effort)

1. **Register open** and **close** — cashier opens their drawer after login;
   closes it (via the Close button *or* the logout guard's forced close).
2. **Deposit / withdrawal** and any till pay-in / withdraw (incl. the recharge
   wallet deposit/withdrawal buttons — they go through the same service methods).
3. **Cash sale completed** — when the sale took cash (cash or mixed tender).
4. **Money receipt paid from the till drawer** — a counter supplier payment,
   expense, or refund whose source is the drawer (not a locker/bank).
5. **Manual "Open Drawer" (No Sale) button** — a cashier button, shown only when
   `open_cash_drawer` is on, that kicks the drawer with no transaction. **Audited**
   (a no-sale drawer-open is a theft vector; the owner gets a trail).

**Never kicks:** receipt reprints/views; card/credit-only sales; money paid from a
locker or bank (the physical drawer is not involved).

## Components

### 1. Setting (migration `0056_cash_drawer.sql`)
Additive, no backfill, inert by default:
```sql
ALTER TABLE settings ADD COLUMN open_cash_drawer BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE settings ADD COLUMN drawer_kick_pin  SMALLINT NOT NULL DEFAULT 0; -- 0 = pin 2, 1 = pin 5
```
Threaded into `settings.Settings` (`open_cash_drawer`, `drawer_kick_pin`) and
`UpdateInput` (form fields), with a toggle + pin select in the Settings printing
section. Rollback = one `goose down`.

### 2. Primitive — `internal/escpos`
```go
// DrawerKick returns the ESC/POS pulse that opens the cash drawer wired to the
// receipt printer (ESC p m t1 t2), or nil when the shop hasn't enabled it. m is
// the drawer pin (0 = pin 2, 1 = pin 5); t1/t2 are on/off times in 2 ms units.
func DrawerKick(cfg settings.Settings) []byte {
    if !cfg.OpenCashDrawer { return nil }
    m := byte(0); if cfg.DrawerKickPin == 1 { m = 1 }
    return []byte{0x1B, 0x70, m, 0x19, 0xFA} // ~50 ms on, ~500 ms off
}
```

### 3. Transport helper — send a kick-only pulse
A single best-effort sender used by every non-print trigger (open/close/deposit/
withdraw/no-sale), so a printer hiccup never fails the money transaction:
```go
func KickDrawer(ctx, cfg) { if b := DrawerKick(cfg); b != nil { _ = printing.Raw(ctx, cfg.ReceiptPrinter, b) } }
```
Cross-platform for free: `printing.Raw` is CUPS raw on Unix and **winspool RAW**
on Windows — the pulse bytes pass through unmodified on both.

### 4. Wiring the triggers

- **A `DrawerKicker func(ctx)` closure** built once in `main.go`, closing over the
  settings service + printing: it reads current settings and sends the pulse.
- **`cashregister.Service.WithDrawerKick(fn)`** — new DI hook (mirrors the existing
  `WithLockerLeg` / `WithSystemPINValidator`). `Service.Open`, `Close`, `PayIn`,
  `Withdraw` call `fn(ctx)` **after** their tx commits. Covers triggers 1 & 2
  (including recharge deposit/withdrawal, which already route through PayIn/
  Withdraw). A rolled-back tx never reaches the kick.
- **Cash sale (trigger 3):** at the cashier sale-completion path, call the kicker
  when the completed sale's tender includes cash. Card/credit-only → no call.
- **Till-paid money receipts (trigger 4):** at the counter supplier-pay / expense /
  refund handlers, call the kicker when the validated source is a till
  (`cashflow.Location.Kind == KindTill`). No double-kick with the cashregister hook
  — these flows don't go through `Service.PayIn/Withdraw`.
- **Manual No-Sale (trigger 5):** `POST /cashier/drawer/open` (JWT + open-drawer
  setting on) → kick + `audit` "opened drawer (no sale)". A button in the cashier
  shell, rendered only when `open_cash_drawer` is on.
- **Test print** gains the kick so the drawer is verifiable from Settings (incl.
  on Windows).

## Error handling

Every kick is **best-effort and post-commit**: the pulse is fire-and-forget; a
printer that is offline, missing, or has no drawer attached produces no error the
user sees and never rolls back or fails the money transaction. When
`open_cash_drawer` is off, `DrawerKick` returns nil and every path is a no-op
(byte-identical to today).

## Testing

- **Unit:** `DrawerKick` returns the right 5 bytes for pin 2 and pin 5, and nil when
  disabled. (`internal/escpos`)
- **Unit:** the No-Sale handler is gated by the setting (403/404 when off) and
  writes an audit row.
- **Live E2E (dev, ESC/POS emulator on :9100 / viewer :8631):** enable the setting;
  confirm the kick bytes reach the emulator on — register open, register close, a
  recharge deposit, a recharge withdrawal, a cash sale, a till-paid expense, and
  the No-Sale button; confirm **no** kick on a card sale, a receipt reprint, and a
  locker-paid expense. Confirm disabled = zero kick bytes anywhere.

## Out of scope

- Drawer-status read-back (whether the drawer is physically open) — most kits don't
  wire the sense line and the printer transport is one-way here.
- Auto-kick on card/credit sales, or on locker/bank-paid receipts.
- A second (USB-direct) drawer not wired through the receipt printer.
```
