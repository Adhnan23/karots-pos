# Rotating Support PIN Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the developer support-account PIN rotate every hour (stable install ID, time-windowed code) so a PIN someone observed cannot be reused later.

**Architecture:** A per-shop seed `HMAC(masterSecret, installID)` is baked into shop builds (never the master). The PIN is `HMAC(seed, current-hour-number)` truncated to 6 digits — validated on the server for the current hour ±1. The System user's login stops using a static `pin_hash` and is routed through an injected time-based validator. `POS_SYSTEM_PIN` stays a static override; the fallback `2273` stays for misbuilt binaries.

**Tech Stack:** Go, `crypto/hmac` + `crypto/sha256`, sqlx/Postgres, Echo, Templ. No new dependencies. No new migration (`settings.install_id` already exists, migration 0055).

## Global Constraints

- Master secret (`POS_SUPPORT_SECRET`) MUST NOT be compiled into a shop binary or written to a shop's `.env` — only the derived per-shop seed is baked. (Preserves commit `b551b96`.)
- Do not add a migration; `settings.install_id` exists and stays stable.
- Six-digit codes are strings everywhere (leading zeros are significant); never ints.
- Install IDs are normalised with `support.Normalise` before any derivation.
- Derivation label is versioned: `karots-pos/support/seed/v1|`.
- Window = 3600s (1 hour); validation skew = ±1 window.
- Constant-time comparison (`hmac.Equal`) for all PIN checks.
- Each git commit message ends with:
  `Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>`

## File Structure

- `internal/support/pin.go` — MODIFY: seed + time-based code + validation (the one place that knows the scheme). Remove the now-unused static `DerivePIN`.
- `internal/support/pin_test.go` — MODIFY: replace `DerivePIN` tests with seed/code/validate tests + shared vectors.
- `internal/features/auth/service.go` — MODIFY: inject a system-PIN validator; branch `Login`/`VerifyCredentials` on `IsSystem`.
- `internal/features/auth/service_systempin_test.go` — CREATE: the branch is exercised with a stub validator (DB-backed, mirrors existing repo tests).
- `cmd/server/support_pin.go` — MODIFY: `supportSeedHex` baked var, `resolveSupportSeed`, `systemPINValidator`, rotating `printSupportPIN`.
- `cmd/server/support_pin_test.go` — CREATE: `systemPINValidator` resolution order (override / seed / 2273).
- `cmd/server/system_admin.go` — MODIFY: store a random placeholder `pin_hash` (login no longer uses it for the System user).
- `cmd/server/main.go` — MODIFY: resolve seed once, log source + server time, wire validator onto `authSvc`.
- `cmd/bootstrap/main.go` — MODIFY: bake the seed (not a bcrypt hash); print the current rotating PIN + note.
- `templates/pages/auth/login.templ` — MODIFY: console-log the server's current time next to the install ID.
- `internal/web/auth.go` — MODIFY: pass server time into `LoginPage`.
- `Makefile`, `.env.example`, `.env.production.example` — MODIFY: doc/comment updates.

---

### Task 1: support seed, rotating code, and validation

**Files:**
- Modify: `internal/support/pin.go`
- Test: `internal/support/pin_test.go`

**Interfaces:**
- Consumes: nothing new.
- Produces:
  - `func DeriveSeed(secret, installID string) []byte` — 32-byte per-shop seed.
  - `func Code(seed []byte, t time.Time) string` — 6-digit code for the hour containing `t`.
  - `func CodeForSecret(secret, installID string, t time.Time) string` — `Code(DeriveSeed(...), t)`.
  - `func Valid(seed []byte, input string, now time.Time, skew int) bool` — accepts `input` for the current window ±`skew`, constant-time.
  - Keeps `Normalise`, `NewInstallID`. Removes `DerivePIN`.

- [ ] **Step 1: Write the failing tests**

Replace the body of `internal/support/pin_test.go` with:

```go
package support

import (
	"testing"
	"time"
)

const secret = "test-master-secret-at-least-32-chars!!"

// A frozen instant so vectors are reproducible. 2026-07-27T09:30:00Z.
var frozen = time.Date(2026, 7, 27, 9, 30, 0, 0, time.UTC)

func TestDeriveSeedIsStableAndPerInstall(t *testing.T) {
	a1 := DeriveSeed(secret, "A1B2C3D4")
	a2 := DeriveSeed(secret, "  a1b2c3d4 ") // case/space-insensitive via Normalise
	if string(a1) != string(a2) {
		t.Fatal("seed must be stable regardless of case/whitespace")
	}
	if len(a1) != 32 {
		t.Fatalf("seed length = %d, want 32", len(a1))
	}
	if string(DeriveSeed(secret, "99887766")) == string(a1) {
		t.Fatal("different installs must produce different seeds")
	}
	if string(DeriveSeed("other-master-secret-at-least-32ch!!", "A1B2C3D4")) == string(a1) {
		t.Fatal("different master secrets must produce different seeds")
	}
}

func TestCodeIsSixDigitsAndRotatesHourly(t *testing.T) {
	seed := DeriveSeed(secret, "A1B2C3D4")
	now := Code(seed, frozen)
	if len(now) != 6 {
		t.Fatalf("code %q is not 6 digits", now)
	}
	for _, r := range now {
		if r < '0' || r > '9' {
			t.Fatalf("code %q is not all digits", now)
		}
	}
	// Same hour → same code.
	if Code(seed, frozen.Add(20*time.Minute)) != now {
		t.Fatal("code must not change within the same hour")
	}
	// Next hour → (almost certainly) different code.
	if Code(seed, frozen.Add(time.Hour)) == now {
		t.Fatal("code must change across the hour boundary")
	}
}

func TestValidAcceptsCurrentAndAdjacentWindows(t *testing.T) {
	seed := DeriveSeed(secret, "A1B2C3D4")
	// A code generated an hour ago must still validate now (skew 1).
	prev := Code(seed, frozen.Add(-time.Hour))
	if !Valid(seed, prev, frozen, 1) {
		t.Fatal("previous-hour code must be accepted with skew 1")
	}
	next := Code(seed, frozen.Add(time.Hour))
	if !Valid(seed, next, frozen, 1) {
		t.Fatal("next-hour code must be accepted with skew 1")
	}
	if !Valid(seed, Code(seed, frozen), frozen, 1) {
		t.Fatal("current code must be accepted")
	}
	// Two hours away is outside the window.
	if Valid(seed, Code(seed, frozen.Add(-2*time.Hour)), frozen, 1) {
		t.Fatal("two-hours-old code must be rejected with skew 1")
	}
	if Valid(seed, "000000", frozen, 1) && Code(seed, frozen) != "000000" {
		t.Fatal("a wrong code must be rejected")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/support/ -run 'TestDeriveSeed|TestCode|TestValid' -v`
Expected: FAIL to compile — `DeriveSeed`, `Code`, `Valid` undefined.

- [ ] **Step 3: Implement in `internal/support/pin.go`**

Replace `DerivePIN` (delete it) and add the following. Keep `Normalise`, `NewInstallID`, and the existing imports plus `time`:

```go
// seedDerivation is versioned so a future change to the scheme cannot silently
// shift the PIN for an install already in the field.
const seedDerivation = "karots-pos/support/seed/v1|"

// windowSeconds is the rotation period: the PIN changes every hour.
const windowSeconds = 3600

// DeriveSeed computes a shop's stable per-shop seed from the developer's master
// secret and the shop's install id. One-way (HMAC), so a leaked seed reveals
// neither the master secret nor any other shop.
func DeriveSeed(secret, installID string) []byte {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(seedDerivation + Normalise(installID)))
	return mac.Sum(nil)
}

// Code is the six-digit support PIN for the hour containing t. The clock supplies
// the changing ingredient, so the same seed yields a different PIN each hour.
func Code(seed []byte, t time.Time) string {
	return codeForWindow(seed, t.UTC().Unix()/windowSeconds)
}

// CodeForSecret derives the seed then the current code — the developer-side helper.
func CodeForSecret(secret, installID string, t time.Time) string {
	return Code(DeriveSeed(secret, installID), t)
}

// Valid reports whether input matches the code for the window containing now, or
// any window within ±skew (clock-skew and read-latency tolerance). Constant-time.
func Valid(seed []byte, input string, now time.Time, skew int) bool {
	w := now.UTC().Unix() / windowSeconds
	for d := -skew; d <= skew; d++ {
		if hmac.Equal([]byte(input), []byte(codeForWindow(seed, w+int64(d)))) {
			return true
		}
	}
	return false
}

func codeForWindow(seed []byte, window int64) string {
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], uint64(window))
	mac := hmac.New(sha256.New, seed)
	mac.Write(buf[:])
	sum := mac.Sum(nil)
	n := binary.BigEndian.Uint32(sum[:4]) % 1000000
	return fmt.Sprintf("%06d", n)
}
```

Update the package doc comment on line 1–8 to describe seed+rotation instead of a static PIN. Remove now-unused imports if any (`encoding/hex` and `crypto/rand` are still used by `NewInstallID`; keep them).

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/support/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/support/pin.go internal/support/pin_test.go
git commit -m "feat(support): time-windowed rotating support PIN derivation

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

### Task 2: route the System user's login through a time-based validator

**Files:**
- Modify: `internal/features/auth/service.go`
- Test: `internal/features/auth/service_systempin_test.go` (create)

**Interfaces:**
- Consumes: `Service.now Clock` (exists).
- Produces:
  - `Service.systemPIN func(pin string, now time.Time) bool` (new unexported field).
  - `func (s *Service) WithSystemPINValidator(fn func(pin string, now time.Time) bool) *Service`.
  - `Login`/`VerifyCredentials` use the validator when `u.IsSystem` and a validator is set; otherwise bcrypt as before.

- [ ] **Step 1: Write the failing test**

Create `internal/features/auth/service_systempin_test.go`. It uses a real DB like other tests in this repo (`TEST_DATABASE_URL` / the dev DB); it inserts a System user with an unusable hash and asserts the validator decides login. Follow the connection pattern already used by auth/web DB tests in this repo (open `sqlx`, run inside a rolled-back tx is not possible for `Login` which opens its own tx, so create + clean up the row explicitly).

```go
package auth

import (
	"context"
	"os"
	"testing"
	"time"

	appdb "karots-pos/internal/db"

	"github.com/jmoiron/sqlx"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func testDB(t *testing.T) *sqlx.DB {
	t.Helper()
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("DATABASE_URL not set")
	}
	db, err := sqlx.Connect("pgx", url)
	if err != nil {
		t.Skipf("cannot connect: %v", err)
	}
	return db
}

func TestSystemUserLoginUsesValidator(t *testing.T) {
	db := testDB(t)
	defer db.Close()
	ctx := context.Background()

	// A System user whose stored hash can never match (login must go via validator).
	_, err := db.ExecContext(ctx,
		`INSERT INTO users (name, phone, role, pin_hash, is_active, must_change_pin, is_system)
		 VALUES ('SysTest', '0000009999', 'admin', 'unusable-hash', true, false, true)
		 ON CONFLICT (phone) DO UPDATE SET pin_hash='unusable-hash', is_system=true, is_active=true`)
	if err != nil {
		t.Fatalf("seed system user: %v", err)
	}
	defer db.ExecContext(ctx, `DELETE FROM users WHERE phone='0000009999'`)

	svc := NewService(db, "0123456789abcdef0123456789abcdef", time.Hour, time.Hour)
	svc.WithSystemPINValidator(func(pin string, _ time.Time) bool { return pin == "424242" })

	if _, err := svc.Login(ctx, LoginInput{Phone: "0000009999", PIN: "wrong"}); err == nil {
		t.Fatal("wrong PIN accepted for system user")
	}
	if _, err := svc.Login(ctx, LoginInput{Phone: "0000009999", PIN: "424242"}); err != nil {
		t.Fatalf("validator-approved PIN rejected: %v", err)
	}
	_ = appdb.WithTx // keep import if unused elsewhere
}
```

(If `appdb` ends up unused, drop the import and the last line.)

- [ ] **Step 2: Run the test to verify it fails**

Run: `set -a && . ./.env && set +a && go test ./internal/features/auth/ -run TestSystemUserLoginUsesValidator -v`
Expected: FAIL — `WithSystemPINValidator` undefined.

- [ ] **Step 3: Implement**

In `internal/features/auth/service.go`, add the field to `Service`:

```go
	now        Clock
	systemPIN  func(pin string, now time.Time) bool
```

Add the setter (near `NewService`):

```go
// WithSystemPINValidator supplies the time-based check for the hidden System
// recovery account, whose PIN rotates hourly and is therefore NOT validated
// against the static pin_hash column. Wired once at startup from cmd/server.
func (s *Service) WithSystemPINValidator(fn func(pin string, now time.Time) bool) *Service {
	s.systemPIN = fn
	return s
}

// checkPIN validates in.PIN for u: the System account uses the injected rotating
// validator when present; everyone else (and a System account on a build with no
// validator wired) uses the stored bcrypt hash.
func (s *Service) checkPIN(u *User, pin string) bool {
	if u.IsSystem && s.systemPIN != nil {
		return s.systemPIN(pin, s.now())
	}
	return bcrypt.CompareHashAndPassword([]byte(u.PinHash), []byte(pin)) == nil
}
```

Replace the two `bcrypt.CompareHashAndPassword` checks in `Login` (line ~67) and `VerifyCredentials` (line ~85):

```go
	if !s.checkPIN(u, in.PIN) {
		return nil, apperr.Unauthorized("invalid credentials")
	}
```

(Leave the `ChangePIN` bcrypt check at line ~239 untouched — the System account never changes its PIN.)

- [ ] **Step 4: Run the test to verify it passes**

Run: `set -a && . ./.env && set +a && go test ./internal/features/auth/ -v`
Expected: PASS (the new test and existing ones).

- [ ] **Step 5: Commit**

```bash
git add internal/features/auth/service.go internal/features/auth/service_systempin_test.go
git commit -m "feat(auth): validate the System account PIN via an injected rotating check

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

### Task 3: server-side seed resolution, validator, and rotating support-pin printer

**Files:**
- Modify: `cmd/server/support_pin.go`
- Test: `cmd/server/support_pin_test.go` (create)

**Interfaces:**
- Consumes: `support.DeriveSeed`, `support.Valid`, `support.CodeForSecret`, `support.Normalise` (Task 1); `installID(db)` (exists).
- Produces:
  - baked var `supportSeedHex string` (replaces `supportHash`); `installIDBaked` stays.
  - `func resolveSupportSeed(db *sqlx.DB) (seed []byte, source string, err error)`.
  - `func systemPINValidator(seed []byte) func(pin string, now time.Time) bool`.
  - `printSupportPIN(id string)` prints the current rotating PIN + minutes left.

- [ ] **Step 1: Write the failing test**

Create `cmd/server/support_pin_test.go`:

```go
package main

import (
	"testing"
	"time"

	"karots-pos/internal/support"
)

func TestSystemPINValidatorResolutionOrder(t *testing.T) {
	seed := support.DeriveSeed("master-secret-at-least-32-characters!", "A1B2C3D4")
	now := time.Date(2026, 7, 27, 9, 30, 0, 0, time.UTC)

	// Seed present, no override: the current rotating code wins, a stale one loses.
	t.Setenv("POS_SYSTEM_PIN", "")
	v := systemPINValidator(seed)
	if !v(support.Code(seed, now), now) {
		t.Fatal("current rotating code should validate")
	}
	if v(support.Code(seed, now.Add(-2*time.Hour)), now) {
		t.Fatal("two-hours-stale code should be rejected")
	}

	// Override set: only the override validates; the rotating code stops working.
	t.Setenv("POS_SYSTEM_PIN", "778899")
	v = systemPINValidator(seed)
	if !v("778899", now) {
		t.Fatal("POS_SYSTEM_PIN override should validate")
	}
	if v(support.Code(seed, now), now) {
		t.Fatal("rotating code must be rejected while override is set")
	}

	// No seed, no override: the 2273 fallback validates.
	t.Setenv("POS_SYSTEM_PIN", "")
	v = systemPINValidator(nil)
	if !v("2273", now) {
		t.Fatal("2273 fallback should validate when no seed and no override")
	}
	if v("000000", now) {
		t.Fatal("a wrong PIN must be rejected in fallback mode")
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./cmd/server/ -run TestSystemPINValidatorResolutionOrder -v`
Expected: FAIL — `systemPINValidator` undefined.

- [ ] **Step 3: Implement in `cmd/server/support_pin.go`**

Rewrite the file. Rename `supportHash` → `supportSeedHex`; drop the bcrypt import (no longer used here). New body:

```go
package main

import (
	"crypto/hmac"
	"encoding/hex"
	"fmt"
	"os"
	"time"

	"karots-pos/internal/support"

	"github.com/jmoiron/sqlx"
)

// A shipped binary carries ONLY its own shop's SEED — HMAC(masterSecret, installID)
// — never the master secret. The seed is one-way, so cracking one shop's binary
// yields nothing about the master or any other shop. The support PIN is derived
// from the seed and the current hour, so it rotates and an observed PIN expires.
var (
	installIDBaked = ""
	supportSeedHex = ""
)

// resolveSupportSeed finds this boot's per-shop seed and describes its source.
// A shipped binary has it baked; the developer's own machine derives it from the
// master secret in .env; a bare build has neither (validator falls back to 2273).
func resolveSupportSeed(db *sqlx.DB) (seed []byte, source string, err error) {
	if supportSeedHex != "" {
		b, derr := hex.DecodeString(supportSeedHex)
		return b, "baked in at build for install " + installIDBaked, derr
	}
	if secret := os.Getenv("POS_SUPPORT_SECRET"); secret != "" {
		id, ierr := installID(db)
		if ierr == nil && id != "" {
			return support.DeriveSeed(secret, id), "derived from POS_SUPPORT_SECRET for install " + id, nil
		}
	}
	return nil, "", nil
}

// systemPINValidator returns the login check for the System account.
// Resolution order: POS_SYSTEM_PIN override → hourly rotating code (±1 hour) →
// fixed 2273 fallback when the build has no seed at all.
func systemPINValidator(seed []byte) func(pin string, now time.Time) bool {
	override := os.Getenv("POS_SYSTEM_PIN")
	return func(pin string, now time.Time) bool {
		if override != "" {
			return hmac.Equal([]byte(pin), []byte(override))
		}
		if seed != nil {
			return support.Valid(seed, pin, now, 1)
		}
		return hmac.Equal([]byte(pin), []byte("2273"))
	}
}

// installID reads this shop's identifier (migration 0055 generates one).
func installID(db *sqlx.DB) (string, error) {
	var id string
	err := db.Get(&id, `SELECT COALESCE(install_id,'') FROM settings ORDER BY id LIMIT 1`)
	return id, err
}

// adoptBakedInstallID makes the database agree with the id the binary was built
// for, so the id the shop reads out is the one -support-pin expects.
func adoptBakedInstallID(db *sqlx.DB) error {
	if installIDBaked == "" {
		return nil
	}
	_, err := db.Exec(
		`UPDATE settings SET install_id = $1 WHERE COALESCE(install_id,'') <> $1`,
		support.Normalise(installIDBaked))
	return err
}

// printSupportPIN answers "the shop is on the phone reading me their install id,
// what is their PIN right now?". The PIN rotates hourly, so it also prints how
// long this one is valid. The master secret comes from the environment at the
// moment it is needed and is never compiled into anything.
func printSupportPIN(id string) {
	secret := os.Getenv("POS_SUPPORT_SECRET")
	if secret == "" {
		fmt.Println("POS_SUPPORT_SECRET is not set — run this on your own machine, where .env has it")
		fmt.Println("  make support-pin ID=" + support.Normalise(id))
		return
	}
	now := time.Now()
	mins := 60 - (now.UTC().Unix()%3600)/60
	fmt.Printf("install %s → support PIN %s  (rotates hourly; valid ~%d more min, previous/next also accepted)\n",
		support.Normalise(id), support.CodeForSecret(secret, id, now), mins)
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./cmd/server/ -run TestSystemPINValidatorResolutionOrder -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/server/support_pin.go cmd/server/support_pin_test.go
git commit -m "feat(support): resolve per-shop seed and validate the rotating PIN server-side

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

### Task 4: wire the validator, placeholder hash, and server-time log

**Files:**
- Modify: `cmd/server/system_admin.go`
- Modify: `cmd/server/main.go:143`, `cmd/server/main.go:168` area

**Interfaces:**
- Consumes: `resolveSupportSeed`, `systemPINValidator` (Task 3); `authSvc.WithSystemPINValidator` (Task 2).
- Produces: the running server accepts the rotating System PIN; boot log states the source and the server's current time.

- [ ] **Step 1: Update `ensureSystemAdmin` to store a placeholder hash**

The System user's `pin_hash` is no longer used for login (the validator decides), so it must be an unusable random value that fails closed if the validator is ever missing. In `cmd/server/system_admin.go`:

- Remove the `hash, source, err := supportCredential(db)` block and the source/SECURITY logging (both move to `main.go`).
- Add, before the row upsert:

```go
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return err
	}
	ph, err := bcrypt.GenerateFromPassword(buf, bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	hash := string(ph)
```

Use `hash` in the existing INSERT/UPDATE statements (unchanged otherwise). Add imports `crypto/rand` and `golang.org/x/crypto/bcrypt`; drop the `support`-based credential call. Keep `adoptBakedInstallID(db)` at the top.

- [ ] **Step 2: Wire the validator + logging in `main.go`**

After `authSvc := auth.RegisterAPI(...)` (line ~168) and before `web.RegisterUI(...)` (line ~197), add:

```go
	seed, source, err := resolveSupportSeed(sqlxDB)
	if err != nil {
		log.Fatalf("support seed: %v", err)
	}
	if source == "" {
		log.Println("SECURITY: this build has no support seed of its own, so its support " +
			"PIN is the fixed fallback 2273. Build shop binaries with `make bootstrap`.")
	} else {
		log.Printf("system admin: support PIN rotates hourly, %s", source)
	}
	log.Printf("server time is %s (the support PIN is derived from this clock)",
		time.Now().UTC().Format(time.RFC3339))
	authSvc.WithSystemPINValidator(systemPINValidator(seed))
```

Ensure `time` and `log` are imported in `main.go` (both likely already are).

- [ ] **Step 3: Build and run the resolution suites**

Run: `go build ./... && set -a && . ./.env && set +a && go test ./cmd/server/ ./internal/support/ ./internal/features/auth/`
Expected: build OK, tests PASS.

- [ ] **Step 4: Live smoke check**

```bash
pkill -f 'bin/karots-pos' 2>/dev/null; sleep 1
make build
./bin/karots-pos &
sleep 3
# derive the current PIN the dev way and confirm the running server accepts it
PIN=$(set -a && . ./.env && set +a && go run ./cmd/server -support-pin 82C920B2 | grep -oE 'PIN [0-9]{6}' | grep -oE '[0-9]{6}')
curl -s -o /dev/null -w "%{http_code}\n" -X POST http://localhost:3000/login \
  --data-urlencode "phone=0000000001" --data-urlencode "pin=$PIN"   # expect 303
curl -s -o /dev/null -w "%{http_code}\n" -X POST http://localhost:3000/login \
  --data-urlencode "phone=0000000001" --data-urlencode "pin=000000"  # expect 200 (re-render, rejected)
pkill -f 'bin/karots-pos'
```

Expected: valid rotating PIN → `303`; wrong PIN → `200` (login re-rendered). If the install id differs from `82C920B2`, read it from the boot log / login console first.

- [ ] **Step 5: Commit**

```bash
git add cmd/server/system_admin.go cmd/server/main.go
git commit -m "feat(support): wire the rotating System-PIN validator and log server time

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

### Task 5: bootstrapper bakes the seed and prints the current PIN

**Files:**
- Modify: `cmd/bootstrap/main.go:157`, `:162`, `:185-188`

**Interfaces:**
- Consumes: `support.DeriveSeed`, `support.CodeForSecret` (Task 1); baked var name `supportSeedHex` (Task 3).
- Produces: shop builds carry `-X main.supportSeedHex=<hex>` (never the master); build output shows the current rotating PIN.

- [ ] **Step 1: Bake the seed instead of a bcrypt hash**

At `cmd/bootstrap/main.go:157`, replace the PIN derivation:

```go
		shopInstallID, shopPIN = id, support.CodeForSecret(supportSecret, id, time.Now())
```

At `:162`, replace the ldflags line (remove the bcrypt `hash` computation just above it if present):

```go
		ldflags += " -X main.installIDBaked=" + id +
			" -X main.supportSeedHex=" + hex.EncodeToString(support.DeriveSeed(supportSecret, id))
```

Ensure imports include `encoding/hex` and `time`; remove `golang.org/x/crypto/bcrypt` if it is now unused in this file.

- [ ] **Step 2: Update the printed guidance**

Around `:185-188`, make the printout state that the PIN rotates:

```go
		fmt.Printf("  support login   0000000001 / %s   (rotates hourly)\n", shopPIN)
		fmt.Printf("  install id      %s\n", shopInstallID)
		fmt.Printf("  recover later   make support-pin ID=%s\n", shopInstallID)
```

(Adjust to match the existing surrounding print style.)

- [ ] **Step 3: Verify a bootstrap build carries the seed, not the master**

Run:
```bash
set -a && . ./.env && set +a && go run ./cmd/bootstrap -plugins recharge -name test-shop
# inspect the produced binary in dist/
strings dist/test-shop* 2>/dev/null | grep -i "$(grep POS_SUPPORT_SECRET .env | cut -d= -f2)" && echo "LEAK" || echo "no master in binary"
go version -m dist/test-shop* 2>/dev/null | grep -i supportSeedHex && echo "seed baked (expected)"
```
Expected: `no master in binary`; the ldflags line shows `supportSeedHex` (the seed — that is the accepted per-shop value), and NOT `POS_SUPPORT_SECRET`. Delete `dist/test-shop*` afterward.

- [ ] **Step 4: Commit**

```bash
git add cmd/bootstrap/main.go
git commit -m "feat(support): bootstrapper bakes the per-shop seed for the rotating PIN

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

### Task 6: login-page server-time console log + doc updates + final E2E

**Files:**
- Modify: `templates/pages/auth/login.templ:9,76-77`
- Modify: `internal/web/auth.go:40` (`ShowLogin`)
- Modify: `Makefile:25-29`, `.env.example`, `.env.production.example`

**Interfaces:**
- Consumes: nothing new.
- Produces: the login page logs the server's current time next to the install id; docs reflect hourly rotation.

- [ ] **Step 1: Add server time to the login console log**

In `templates/pages/auth/login.templ`, change the signature and the console call:

```go
templ LoginPage(errMsg, installID, serverTime string) {
```

and the console helper:

```go
templ installIDConsole(installID, serverTime string) {
	@templ.JSFuncCall("console.info", "POS install "+installID+" · server time "+serverTime)
}
```

Update the call site (`@installIDConsole(installID, serverTime)`) inside `LoginPage`.

- [ ] **Step 2: Pass server time from `ShowLogin`**

In `internal/web/auth.go`, update `ShowLogin`:

```go
func (h *authUI) ShowLogin(c echo.Context) error {
	return response.RenderPage(c, authpages.LoginPage("", h.installID(c),
		time.Now().UTC().Format(time.RFC3339)))
}
```

Find the other `LoginPage(...)` call(s) in `internal/web/auth.go` (e.g. `loginError`) and add the same `time.Now().UTC().Format(time.RFC3339)` third argument. Ensure `time` is imported.

- [ ] **Step 3: Regenerate templ and build**

Run: `templ generate && go build ./...`
Expected: no errors.

- [ ] **Step 4: Update Makefile + env docs**

- `Makefile` `support-pin` comment (lines ~25-29): note the PIN it prints is the current hour's and rotates.
- `.env.example`: near `POS_SUPPORT_SECRET`, note the support PIN rotates hourly and validation tolerates ±1 hour of clock skew.
- `.env.production.example`: note `POS_SYSTEM_PIN` (commented) is the clock-independent emergency override if the server clock is wrong; still must never contain `POS_SUPPORT_SECRET`.

- [ ] **Step 5: Full test + live E2E**

Run:
```bash
set -a && . ./.env && set +a && go test ./...
pkill -f 'bin/karots-pos' 2>/dev/null; sleep 1; make build; ./bin/karots-pos & sleep 3
# Rotating PIN accepted:
PIN=$(set -a && . ./.env && set +a && go run ./cmd/server -support-pin 82C920B2 | grep -oE '[0-9]{6}' | head -1)
curl -s -o /dev/null -w "valid=%{http_code}\n" -X POST http://localhost:3000/login \
  --data-urlencode phone=0000000001 --data-urlencode pin=$PIN            # 303
# Override wins:
pkill -f 'bin/karots-pos'; sleep 1; POS_SYSTEM_PIN=778899 ./bin/karots-pos & sleep 3
curl -s -o /dev/null -w "override=%{http_code}\n" -X POST http://localhost:3000/login \
  --data-urlencode phone=0000000001 --data-urlencode pin=778899          # 303
curl -s -o /dev/null -w "rotating-blocked=%{http_code}\n" -X POST http://localhost:3000/login \
  --data-urlencode phone=0000000001 --data-urlencode pin=$PIN            # 200 (rejected)
pkill -f 'bin/karots-pos'
```
Expected: `valid=303`, `override=303`, `rotating-blocked=200`. Also open `http://localhost:3000/login` in a browser and confirm the console shows `POS install … · server time …`.

- [ ] **Step 6: Commit**

```bash
git add templates/pages/auth/login.templ internal/web/auth.go Makefile .env.example .env.production.example
git commit -m "feat(support): expose server time on login for clock-skewed shops; doc rotation

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Self-Review

- **Spec coverage:** seed derivation (T1), rotating code + ±1 validation (T1), server resolution order incl. POS_SYSTEM_PIN + 2273 fallback (T3), System-user login via validator (T2/T4), placeholder hash / no static PIN for shop builds (T4), bootstrapper bakes seed not master (T5), `make support-pin` prints current rotating PIN (T3), server-time exposure for clock skew (T4 boot log, T6 login console), env/Makefile docs (T6), no migration. App custom-time entry is explicitly deferred (spec Out of scope). ✔ all covered.
- **Placeholder scan:** none — every code step has concrete code.
- **Type consistency:** `supportSeedHex` used in T3 (declare) and T5 (bake); `systemPINValidator(seed []byte) func(string, time.Time) bool` matches `WithSystemPINValidator` param in T2; `resolveSupportSeed` returns `([]byte, string, error)` consumed in T4; `Code/DeriveSeed/CodeForSecret/Valid` signatures identical across T1/T3/T5. ✔
