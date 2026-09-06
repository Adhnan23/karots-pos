# AI Catalog Assistant — Design Spec

**Date:** 2026-09-06
**Status:** Approved for Phase 1 implementation
**Type:** New optional plugin (`plugins/aicatalog/`)

## Problem

A shop owner (real case: a mechanical spare-parts shop) doesn't know what half
their stock *is* or how to categorize it. A code like `6200 2RS` means nothing
to them — it's a deep-groove ball bearing (10mm bore, 30mm OD, 9mm wide, rubber
sealed both sides), but they can't name it, price a category tree for it, or
decide where it belongs. The existing stock-intake page assumes you already know
the product. We need an assistant that identifies cryptic parts, proposes a
sensible category, and captures the product with as little typing as possible.

## Decisions (from brainstorming)

- **Plugin, not a separate app.** Everything it needs already lives in the POS
  (products, categories, barcode gen, label print, stock intake). A separate app
  would re-plumb all of it and risk catalog drift. Plugin reuses core directly.
- **Provider-agnostic AI over one OpenAI-compatible HTTP client.** Provider,
  base URL, model, and API key are plugin settings. Default to Google Gemini
  free tier (quality + can add search grounding later); an OpenRouter key + free
  model works with the same code. No lock-in, one client either way.
- **Learn once.** When an item's identity is settled (AI-confident, user-picked,
  or user-explained) the plugin stores what it learned on that product. The
  Phase-2 Optimize pass reads that memory and never re-asks a settled item.
- **Phase 1 = the daily-driver quick-add wizard.** The batch Optimize is Phase 2,
  designed after we've seen real AI output.
- **Conversational stepper, not a big form.** One thing on screen at a time,
  mostly tap-to-pick options the AI offers, with an "Other" escape to type.
  Keyboard only for the first "what is it" box and the numbers.

## Scope

### In scope (Phase 1)
- New plugin shell (`plugin.json`, registration mirroring `plugins/clearance/`).
- Settings (provider, base URL, model, API key, default markup) + "Test key".
- OpenAI-compatible AI client (`ai.go`), returning strict JSON.
- Quick-add stepper page at `/admin/ai-catalog`.
- Two tables (`aicatalog_settings`, `aicatalog_items`) via one migration.
- Reuse of core: product intake, category find-or-create, barcode
  generate/dup-check/assign, existing label printing.

### Out of scope (Phase 2+, explicitly deferred)
- Batch **Optimize** (re-scan catalog; create specific child categories under
  broad ones; **merge** redundant categories; **move** items). Note:
  `internal/features/categories` currently has `Create` and `FindOrCreateByPath`
  but **no Merge/Move** — Phase 2 must add those.
- Photo / vision identification.
- Web-search grounding (Gemini native grounding, beyond plain chat).

## Architecture

Self-contained plugin. Core stays inert without it (compile-time plugin rule).
Follows the clearance plugin's shape: `plugin.json`, a `Plugin` that registers
an admin page + routes + a settings section, a `Store`, `pages.templ`, migrations.

```
plugins/aicatalog/
  plugin.json
  plugin.go            # registration: route(s), nav, settings section
  admin.go             # HTTP handlers for each stepper step + settings save
  ai.go                # OpenAI-compatible client; Identify() -> IdentifyResult
  store.go             # settings get/save; item learn/get; markup + parse helpers
  pages.templ          # stepper UI (HTMX + Alpine), settings form
  migrations/
    0001_init.sql
  store_test.go        # markup math + AI JSON parse + dup-guard
```

### Components

**`ai.go` — the AI client.** One `POST` to `{base_url}/chat/completions`
(OpenAI-compatible; Gemini and OpenRouter both speak it) using stdlib
`net/http` + `encoding/json`. No new dependency.

- `Identify(ctx, query, userHint string) (IdentifyResult, error)`
- Prompt instructs the model to answer as **strict JSON only**:
  ```json
  {
    "confident": true,
    "best":    {"name":"", "category":"", "specs":"", "explanation":""},
    "options": [{"name":"", "category":"", "specs":"", "explanation":""}]
  }
  ```
  `options` holds up to 4 when not confident. `category` is a path suitable for
  `FindOrCreateByPath` (e.g. `Bearings/Deep Groove`).
- Robust parse: strip code fences / leading prose, then `json.Unmarshal`. On
  parse failure or HTTP error return a typed error so the page degrades to
  manual entry (AI assists, never gates).
- Context timeout (e.g. 20s). API key read from settings, **server-side only**.

**`store.go` — data + pure helpers.**
- `GetSettings` / `SaveSettings`.
- `LearnItem(productID, resolved, category, specs, explanation, source, rawQuery)`
  and `GetItem(productID)` — the learn-once memory.
- `costFromMarkup(selling, markup decimal.Decimal) decimal.Decimal` =
  `round(selling / markup)`. Guards: `markup > 1`, no divide-by-zero.
- `parseIdentify([]byte) (IdentifyResult, error)` — testable JSON parse.

**`admin.go` — stepper handlers (HTMX fragments, one step per response).**
Each step returns just the next step card. Server holds no wizard session; the
partial state rides hidden form fields between steps (name, category, barcode,
cost, qty). Steps:
1. `GET  /admin/ai-catalog` — page shell + step 1 ("What is it?").
2. `POST /admin/ai-catalog/identify` — calls `ai.Identify`, renders the
   confident match or up-to-4 option chips + "None — let me explain".
3. `POST /admin/ai-catalog/pick` — records chosen identity (or user explanation),
   renders the **Category** step (AI suggestion chip + alternates + Other).
4. `POST /admin/ai-catalog/category` — renders the **Barcode** step
   (Scan / Generate). Scan input placed **last** so the scanner's trailing Enter
   doesn't submit early (known codebase gotcha).
5. `POST /admin/ai-catalog/barcode` — dup-check via `BarcodeExists`; if taken,
   warn + offer Generate; renders the **Price** step (cost, or selling + markup
   chip → auto-cost).
6. `POST /admin/ai-catalog/price` — renders the **Qty** step.
7. `POST /admin/ai-catalog/qty` — renders the **Labels** step (preset chips:
   = qty / a few / custom / none).
8. `POST /admin/ai-catalog/save` — `CreateForIntake` (product + qty) →
   `AssignBarcode` → `LearnItem` → fire label print for N via existing label
   flow → `htmxDone` toast → reset to step 1.
9. `POST /admin/ai-catalog/settings` — save settings.
10. `POST /admin/ai-catalog/test-key` — tiny AI ping, returns ok/fail toast.

### Data flow (happy path)

```
type "6200 2rs"
  -> identify -> AI JSON {confident, best/options}
  -> pick option (or explain)
  -> confirm category (FindOrCreateByPath on save)
  -> scan/generate barcode (BarcodeExists dup-check)
  -> price: selling 500 x1.4 -> cost round(500/1.4)=357
  -> qty on hand
  -> labels = qty
  -> save: CreateForIntake + AssignBarcode + LearnItem + print labels
  -> toast, back to step 1
```

## Data model (migration 0001)

```sql
CREATE TABLE aicatalog_settings (
  id             SMALLINT PRIMARY KEY DEFAULT 1 CHECK (id = 1),
  provider       TEXT NOT NULL DEFAULT 'gemini',
  base_url       TEXT NOT NULL DEFAULT 'https://generativelanguage.googleapis.com/v1beta/openai',
  model          TEXT NOT NULL DEFAULT 'gemini-2.5-flash',
  api_key        TEXT NOT NULL DEFAULT '',
  default_markup NUMERIC(6,3) NOT NULL DEFAULT 1.400
);
INSERT INTO aicatalog_settings (id) VALUES (1) ON CONFLICT DO NOTHING;

CREATE TABLE aicatalog_items (
  product_id         BIGINT PRIMARY KEY REFERENCES products(id) ON DELETE CASCADE,
  resolved_name      TEXT NOT NULL,
  suggested_category TEXT NOT NULL DEFAULT '',
  specs              TEXT NOT NULL DEFAULT '',
  user_explanation   TEXT NOT NULL DEFAULT '',
  source             TEXT NOT NULL,   -- ai_confident | user_picked | user_explained
  raw_query          TEXT NOT NULL DEFAULT '',
  created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at         TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

(Default base_url/model are best-guess for Gemini's OpenAI-compatible endpoint;
confirm the exact path/model string during implementation and adjust the default.)

## Reused core (no reinvention)

- `products.Service.CreateForIntake(ctx, IntakeInput, userID)` — product + qty.
- `products.Service.GenerateBarcode`, `Repository.BarcodeExists`,
  `Service.AssignBarcode` — barcode generate / dup-check / assign.
- `categories.Service.FindOrCreateByPath(ctx, path)` — AI category path.
- Existing label print (`sendLabel` / `internal/tspl`) — print N labels.
- Plugin registration + settings section — mirror `plugins/clearance/`.

## Guardrails

- API key **server-side only**; never rendered to the browser.
- AI output is **always human-confirmed** before any catalog write — no silent
  creates, renames, or category changes.
- Barcode dup-checked before assign; on collision, warn + one-click generate.
- Markup math guarded (`markup > 1`, no divide-by-zero); show computed cost back.
- AI/internet failure → error toast; the stepper still lets the user type name,
  category, price, qty by hand. AI is an assist, not a gate.
- Provider-agnostic: switching provider/model/key is settings-only.

## Testing (Phase 1)

- `costFromMarkup`: `500 / 1.4 -> 357`; markup ≤ 1 rejected; no panic on zero.
- `parseIdentify`: sample confident JSON and options JSON (incl. wrapped in code
  fences) parse correctly; garbage returns error, not panic.
- Barcode dup-guard: existing code → warn path, not assign.
- Core-only build (no plugin import) still compiles and is inert.

Small assert-based tests, no framework beyond the existing `_test.go` style.

## Phase 2 (designed later)

Batch **Optimize** button: re-scan the catalog using the stored
`aicatalog_items` memory, propose a cleaner category tree (create specific child
categories, **merge** redundant ones, **move** items), preview + approve each (or
approve all) — never auto-applied. Requires new `categories` Merge/Move. Then:
photo/vision input, and Gemini search grounding for obscure parts.
