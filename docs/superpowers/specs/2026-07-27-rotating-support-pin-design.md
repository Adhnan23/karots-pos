# Rotating support PIN (time-windowed, stable install ID)

Date: 2026-07-27
Status: design, approved for implementation

## Problem

The developer support account is a deliberate backdoor for a self-hosted (non-SaaS)
POS: it lets the developer troubleshoot a shop without borrowing the owner's
credentials, and its actions stay in the owner-readable audit log. See
`support-account-design` / commit `b551b96`.

Today that account's PIN is **static for the life of the binary**:
`PIN = HMAC(masterSecret, "karots-pos/support/v1|" + installID)`, baked as a
bcrypt hash into the shop build. The install ID is stable, so the PIN is stable.

**The threat this spec closes:** the developer visits a shop, logs in with the
support PIN, and someone present notices the six digits. Because the PIN never
changes, that person can reuse it weeks later. A one-time observation becomes
permanent access.

**Not a threat (clarified during design):** a shop person learning the *install
ID*. The ID is not a secret — it is printed to the console on purpose. Turning an
ID into a PIN requires the master secret, which never leaves the developer's
machine. So the fix is to rotate the *PIN*, not the ID.

## Design

Rotate the PIN on a time window — TOTP with a per-shop seed. The install ID stays
stable; the clock supplies the changing ingredient, so nobody has to read a fresh
value out each visit.

```
seed(installID)   = HMAC-SHA256(masterSecret, "karots-pos/support/seed/v1|" + Normalise(installID))   // 32 bytes, stable
window(t)         = floor(unix_seconds(t) / 3600)                                                       // ticks +1 each hour
code(installID,t) = first 4 bytes of HMAC-SHA256(seed, bigEndian_uint64(window)) mod 1e6, "%06d"        // 6 digits, rotating
```

- **Stable ingredient:** the seed depends only on `masterSecret` + `installID`, so
  it never changes for a given shop. The install ID is read from a shop *once*.
- **Rotating ingredient:** `window` is "hours since the Unix epoch" — both the
  shop's server and the developer's tool compute it from their own clock and
  naturally agree. No one reads it aloud.
- **Result:** same ID, a different 6-digit PIN every hour. A PIN someone observed
  is dead within the hour.

### Why the seed is safe to bake

The shop build carries the **seed**, not the master. The seed is one-way from the
master (`HMAC`), so a leaked seed reveals neither the master nor any other shop —
the isolation property from `b551b96` is preserved. Extracting one shop's binary
compromises only that shop (as it does today), and the master stays on the
developer's machine only.

Accepted tradeoff (confirmed with owner): today's binary carries a one-way bcrypt
*hash*, so extraction still requires brute-forcing 6 digits (~hours). A baked
*seed* lets an extractor generate that shop's codes instantly. Same end state (that
one shop), and rotation is worth it: an *observed* PIN now expires. This is the
whole point.

### Clock skew

TOTP is time-sensitive. Mitigations:

- **Validation accepts `window-1`, `window`, `window+1`** (±1 hour). A code read
  near the top of the hour does not expire in the developer's hand, and a modestly
  wrong shop clock still works. A code is therefore valid for ~1–2 hours.
- **`POS_SYSTEM_PIN` remains a static, clock-independent override** (resolution
  order below) — the escape hatch if a shop's clock is wildly wrong or the
  developer is otherwise locked out. It ships commented out and is meant to be
  removed after use.

### Server resolution order

Unchanged in shape from today; the middle rung becomes rotating:

1. `POS_SYSTEM_PIN` env set → accept that exact static PIN (emergency override).
2. A seed is available → accept `code` for `window` ±1 (constant-time compare).
   - **Shop build:** seed baked at build time (`main.supportSeed`, hex).
   - **Dev build (`make build`):** no baked seed; derive the seed at boot from
     `POS_SUPPORT_SECRET` in `.env` (developer machine only).
3. No seed and no override → fall back to `2273` + a loud SECURITY warning every
   boot (unchanged safety net for a misbuilt binary).

The static bcrypt-PIN path for shop builds is **removed** — shop builds are
rotating-only. (`POS_SYSTEM_PIN` is still static by nature; that is intended.)

## Changes (karots-pos, Go)

- **`internal/support/pin.go`**
  - Add `DeriveSeed(secret, installID string) []byte` (the `seed(...)` above).
  - Add `Code(seed []byte, t time.Time) string` and
    `CodeForSecret(secret, installID string, t time.Time) string` (convenience:
    seed then code).
  - Add `Valid(seed []byte, input string, now time.Time, skew int) bool` —
    constant-time compare against `window` ±`skew`.
  - Keep `Normalise`, `NewInstallID`. Keep or retire `DerivePIN`: retained only if
    still referenced; shop builds no longer use it.
  - New versioned label `karots-pos/support/seed/v1|` so the scheme is explicit and
    a future change cannot silently shift a field install's PINs.

- **`cmd/bootstrap/main.go`** — bake `main.supportSeed = hex(DeriveSeed(master, id))`
  instead of a bcrypt hash. `master` still comes from `POS_SUPPORT_SECRET` at build
  time and is never written into the shop artifact or its `.env`. Printed output:
  install id, and the *current* PIN as a convenience (with a note it rotates
  hourly).

- **`cmd/server/support_pin.go`** — replace the bcrypt-hash credential with seed
  resolution + rotating validation (`support.Valid`, ±1 window). Sources: baked
  `supportSeed` → derive from `POS_SUPPORT_SECRET` → none. Keep the
  `POS_SYSTEM_PIN` override ahead of it.

- **`cmd/server/system_admin.go`** — `ensureSystemAdmin` no longer stores a static
  PIN hash for the support user; the login check calls the rotating validator.
  Adopt-baked-install-id logic unchanged. Log which seed source is in use, or the
  SECURITY warning when none.

- **`Makefile`** — `support-pin ID=...` prints the *current* rotating PIN (and the
  minutes left in the window / the adjacent-window codes for convenience). Still
  runs only on the developer machine (needs `POS_SUPPORT_SECRET` from `.env`).

- **`.env.example` / `.env.production.example`** — note that the support PIN now
  rotates hourly; production template still must never contain `POS_SUPPORT_SECRET`.

- **Login footer console log** — unchanged: still prints the stable install ID.

- **No migration.** `settings.install_id` already exists (migration `0055`) and
  stays stable.

## Backward compatibility

Each binary is self-contained. Shops already in the field on the static scheme keep
working on their current binary; they move to rotating PINs only when rebuilt
(`make bootstrap`) — the same event that ships the recharge-menu fix. New builds are
rotating. The companion app (separate spec) records which master key built each
shop, so post-rotation the developer still computes the correct PIN.

## Testing

- Unit tests in `internal/support`:
  - `DeriveSeed` is stable for a fixed (secret, id) and changes with either input;
    case/space-insensitive on the id via `Normalise`.
  - `Code` changes across hour boundaries and repeats within the same hour.
  - `Valid` accepts current and ±1 window, rejects ±2, rejects a wrong code; uses
    constant-time compare.
- **Shared test vectors** committed as a small fixture (secret, installID,
  unix_time → seed hex, PIN). The Flutter app (deliverable #2) asserts the same
  vectors so Dart and Go agree byte-for-byte.
- Live check on the dev server: `make support-pin` PIN logs in; a code from the
  previous-previous window (±2) is rejected; `POS_SYSTEM_PIN` still overrides.

## Out of scope

- The **Flutter companion app** (`karots-project-pins`) — its own spec. Data model
  agreed: Project → one-or-more master keys → Customers (name + install ID + which
  master key built them) → live PIN + countdown; master keys encrypted in the
  Android Keystore, offline-only; parameterised scheme for reuse by future
  projects. New shops are built with the newest master key; old shops keep their
  recorded key unless rebuilt.
- Asymmetric / signature-based support auth (kept 6-digit UX instead).
- Any change to the audit-log visibility of the support account (unchanged: owner
  can read it).
