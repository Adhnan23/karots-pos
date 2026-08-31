# Rotating Support PIN — portable scheme reference

This document defines the **rotating support-PIN protocol** so any project can
implement a compatible server side and be driven by the same authenticator app
("Karots PIN Vault"). karots-pos is the reference implementation; the algorithm
below is deliberately language-agnostic.

The point of the scheme: a self-hosted app ships with a hidden developer/support
login. The PIN must be **per-install** (a code lifted from one deployment is
useless against another) and **rotating** (a code someone observed stops working
within the hour). Nothing per-install is stored on the developer side — only one
master secret, kept off every shipped binary.

## Roles and secrets

- **Master secret** — one long random string per *project*, held ONLY on the
  developer's machine (and, encrypted, inside the authenticator app). Never
  compiled into a shipped binary, never in a deployment's config. Losing it means
  no field install's PIN can be derived any more (keep the emergency override,
  below, and back the secret up).
- **Install ID** — a short, non-secret identifier generated once per install and
  shown to the operator (e.g. printed to a console). Useless without the master
  secret. Read once by the developer and remembered.
- **Per-install seed** — `HMAC-SHA256(masterSecret, label + installID)`. One-way,
  so a leaked seed reveals neither the master secret nor any other install. This
  is the ONLY credential a shipped binary carries.
- **PIN** — six digits derived from the seed and the current clock hour. Rotates.

## Parameters (per project)

| Parameter        | karots-pos value                    | Notes |
|------------------|-------------------------------------|-------|
| HMAC hash        | SHA-256                             | fixed |
| Seed label       | `karots-pos/support/seed/v1|`       | project-specific; version it |
| Window seconds   | `3600` (1 hour)                     | rotation period |
| Digits           | `6`                                 | login form accepts 4–6 |
| Validation skew  | `±1` window                         | tolerate read-latency + clock drift |

A different project uses its **own seed label** (e.g.
`myapp/support/seed/v1|`) and may change window/digits. Everything else is
identical. The authenticator app stores these parameters per project, so one app
serves many projects.

**Generator vs validator — which parameters each side needs.** Only three
parameters go into *producing* a code: **seed label, window seconds, digits**.
Those are all the authenticator (Karots PIN Vault) asks for, and getting the seed
label wrong is the usual cause of a "right length, right rotation, wrong number"
mismatch — Vault defaults the label to `karots-pos/support/seed/v1|`, so for any
other project you must change it. **Validation skew is a server-only parameter**:
it only affects how many neighbouring windows the server's `valid()` will *accept*
(±1 here), and never changes the numbers the generator produces — so the
authenticator has no skew field, and it is not part of matching a code.

## Normalisation

Before use, normalise the install ID so an operator reading it aloud and a
developer typing it back need not match case or spaces:

```
normalise(id) = uppercase(trim(id))
```

## Algorithm

All integers are unsigned big-endian. Time is absolute Unix seconds (UTC).

```
seed(master, id):
    return HMAC_SHA256(key = master, msg = utf8(seedLabel + normalise(id)))   # 32 bytes

window(t):
    return floor(unix_seconds(t) / windowSeconds)                             # int64

code(seed, t):
    w   = window(t)
    mac = HMAC_SHA256(key = seed, msg = uint64_be(w))                         # 32 bytes
    n   = uint32_be(mac[0..4]) mod 1_000_000
    return zero_pad(n, 6)                                                     # string, leading zeros kept

valid(seed, input, now, skew):                                               # server side
    w0 = window(now)
    for d in -skew .. +skew:
        if constant_time_equal(input, code_for_window(seed, w0 + d)):
            return true
    return false
```

`code_for_window(seed, w)` is `code` with the window fixed to `w`. Six digits are
a **string** everywhere (the leading zero in `036581` is significant) — never an
integer. Compare with a **constant-time** equality.

## Server resolution order (recommended)

The login check for the support account should resolve in this order:

1. **Emergency override** — a static PIN from an environment variable
   (karots-pos: `POS_SYSTEM_PIN`). If set, ONLY it is accepted. This is the
   clock-independent way back in when a deployment's clock is badly wrong (the
   PIN is time-based) or the master secret is lost. Ship it commented out.
2. **Rotating PIN** — if a seed is available (baked into a shipped build, or
   derived from the master secret on the developer's own machine), accept
   `valid(seed, input, now, 1)`.
3. **Fallback** — if the build has no seed at all, accept a fixed development PIN
   and log a loud security warning every boot.

The support account's stored password hash is **not** used for its login (the PIN
rotates); store an unusable random placeholder so login fails closed if the
validator is ever unwired.

## Clock skew

The PIN is derived from the server's clock, so validation tolerates `±1` window
(≈1–2 hours of usable life per code). For larger drift (a reset RTC/CMOS clock),
two things help:

- The server should **expose its current time** (boot log and/or login-page
  console) so the developer can see what the deployment thinks the time is.
- The authenticator app can **compute the PIN for a custom time**, matching the
  deployment's wrong clock.

If the clock is hopeless, use the emergency override.

## Security properties

- One shipped binary reveals only its own install's seed — never the master
  secret, never another install (HMAC is one-way).
- An observed PIN expires within the window.
- Do NOT bake the master secret with linker flags: toolchains often record the
  full flag line in build metadata (Go: `go version -m`) where it is readable.
  Bake only the per-install seed.
- The support account's actions should remain in the operator-readable audit log
  — the visibility is the point (it lets the developer prove which changes were
  theirs).

## Shared test vectors

Any implementation MUST reproduce these exactly. Secret:
`test-master-secret-at-least-32-chars!!`, seed label
`karots-pos/support/seed/v1|`, window `3600`, digits `6`.

| install ID | unix time    | seed (hex, first/last 4 bytes)      | PIN     |
|------------|--------------|-------------------------------------|---------|
| `A1B2C3D4` | `1785144600` | `b1b3a7b7…98cb51f1`                 | `241155`|
| `A1B2C3D4` | `1785146400` | `b1b3a7b7…98cb51f1` (same seed)     | `745498`|
| `599FA375` | `946684800`  | `379d3844…52bcceca`                 | `572485`|
| `00000000` | `0`          | `6384cdc1…1c3ddccb`                 | `855209`|

Full seeds:
```
A1B2C3D4 → b1b3a7b731ebaaed5d46d6f42a2814ef834b5dcdf00e14b12eebb40d98cb51f1
599FA375 → 379d38440f8199ce470a8d46370ca7921ed13807ead19048837c9b2952bcceca
00000000 → 6384cdc191cdabe5e6cc84804c78dab81b084b9607f15d37f8c0b7571c3ddccb
```

## Reference implementation

karots-pos: `internal/support/pin.go` (`DeriveSeed`, `Code`, `CodeForSecret`,
`Valid`) and the server wiring in `cmd/server/support_pin.go`. Design notes:
`docs/superpowers/specs/2026-07-27-rotating-support-pin-design.md`.
