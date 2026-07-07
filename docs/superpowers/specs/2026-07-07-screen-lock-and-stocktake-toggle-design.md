# Screen Lock + Stock-take enable/disable — design

Date: 2026-07-07
Status: approved (ready to implement)

Two small, mostly-independent features. **No plugin changes.** Both add one
Settings field (one migration, two columns total).

---

## Feature A — Screen Lock

A lock ≠ logout: the session (and any open till / recharge float) stays open. The
user must re-enter phone + PIN to resume. Locks manually (button) or automatically
after N minutes of inactivity.

### Mechanism — a `locked` JWT claim (mirrors the existing `MustChangePin` gate)

Auth is a stateless JWT in the `pos_token` httpOnly cookie (`internal/middleware/auth.go`,
`Claims`). We add:

```go
Locked bool `json:"lck,omitempty"`
```

- **Lock** re-issues the *same* token with `Locked=true`, **preserving the original
  `ExpiresAt`/`IssuedAt`** (locking must never extend the session). New helper on
  `auth.Service`, e.g. `ReissueLocked(claims *middleware.Claims, locked bool) (string, error)`
  that signs a `Claims` copy with the flag flipped and the original registered
  claims kept. The web layer sets it via the existing `setCookie`.
- **`RequireUnlocked()` middleware** runs immediately after `JWTAuth` (like
  `RequirePinChosen`). While `Locked`, it redirects every request to `/lock`,
  except `/lock`, `/unlock`, `/logout`, and static assets. Added to both the admin
  and cashier route groups.
- Middleware exposes the flag on the context (`ctxLocked`) + `middleware.IsLocked(c)`
  and stores it from the parsed claim, like `ctxMustChangePin`.
- **Why a claim, not a separate cookie flag:** deleting the cookie to bypass the
  lock just logs the user out → back to `/login`, i.e. strictly more locked. A
  signed claim can't be forged.

### `/lock` and `/unlock`

- `POST /lock` (any authenticated role): re-issue token locked, redirect to `/lock`.
- `GET /lock`: full-page lock screen (its own minimal layout, not the app chrome).
  Shows the locked user's name; fields for phone + PIN; **Unlock** button; a
  secondary "Not you? Log out" link → `/logout` (which keeps its cash-open guard).
- `POST /unlock`: verify the entered phone + PIN via the normal
  `auth.Service.Login` credential path. Unlock succeeds when the verified user is
  **the same user (`id == session uid`) OR has the admin role**. On success,
  re-issue the *current session's* token with `Locked=false` (identity unchanged —
  an admin unlocking a cashier's terminal leaves the cashier's shift intact) and
  redirect to that role's home. On failure, re-render `/lock` with an error.
  Rate-limited like login.

### Inactivity + Lock button (client)

- A small Alpine snippet in the admin + cashier base layouts resets a timer on
  `mousemove`/`keydown`/`touchstart`/`click`; after `lock_timeout_minutes` it POSTs
  `/lock` (a hidden form or `fetch` then `location='/lock'`). Skipped entirely when
  the timeout is 0.
- A **Lock** button in each layout's top bar POSTs `/lock` immediately; always
  present regardless of the timeout.

### Settings

- New column `lock_timeout_minutes INT NOT NULL DEFAULT 0` (0 = auto-lock off).
- Add to `settings.Settings`, `UpdateInput`, the update SQL, and one numeric field
  in the Settings form ("Auto-lock after N minutes of inactivity — 0 to disable").

---

## Feature B — Stock-take enable / disable

Stock-take rewrites on-hand quantities and prices; the shop can turn it off.

### Settings

- New column `stock_take_enabled BOOLEAN NOT NULL DEFAULT true` (default true — no
  behaviour change for existing shops).
- Add to `settings.Settings`, `UpdateInput`, update SQL, and a toggle in the
  Settings form ("Enable stock-take").

### When disabled

- **Route gate (the security boundary):** a check at the top of every stock-take
  handler — `StockTake`, `StockTakeApply`, `StockTakeSheet`, `StockTakeImportModal`,
  `StockTakeImport` — returns a **403 "Stock-take is turned off in Settings"** page
  when `!stock_take_enabled`. A pasted URL therefore does nothing. Implemented as a
  tiny helper the handlers call (reads settings once).
- **Card hidden:** the Inventory hub page filters the Stock-take `AdminLink` out of
  its rendered cards, and the command palette omits it, when disabled. The sidebar
  only lists section hubs, so no sidebar change is needed.

---

## Out of scope / non-goals

- No per-user or per-terminal lock policy — one shop-wide timeout.
- No "auto-logout" (that would close sessions — explicitly not wanted).
- Lock does not blur/preserve the underlying screen; it's a clean full-page lock.
- Stock-take toggle governs only the admin stock-take feature, nothing else.

## Files touched (estimate)

- Migration: `+2` columns.
- `internal/features/settings/settings.go` (struct, UpdateInput, update SQL).
- `internal/middleware/auth.go` (Locked claim, ctx, `RequireUnlocked`, `IsLocked`).
- `internal/features/auth/{token,service}.go` (`ReissueLocked`).
- `internal/web/auth.go` + routes in `internal/web/web.go` (`/lock`, `/unlock`,
  wire `RequireUnlocked` into admin+cashier groups).
- Lock page template (new, minimal layout).
- `templates/layouts/{admin,cashier}` base (inactivity snippet + Lock button).
- Settings form template (2 fields).
- Stock-take handlers gate + Inventory hub/palette card filter.
