# AI Catalog — Mock Products staging + chunked Optimize

**Date:** 2026-09-07
**Plugin:** `plugins/aicatalog`
**Status:** design, pending build

Two related additions: (1) a **mock/staging products** table for fast bulk
creation with optional AI batch enrich, and (2) making the existing category
**Optimize** work in **chunks** so it scales past ~300 products.

## Goal

A staging table inside the AI Catalog plugin for **fast bulk product
creation**. You quick-add many draft ("mock") rows with the minimum you know —
name, qty, price — optionally enrich the whole batch through an AI (API key or
manual copy-paste), review, then **commit** them into real products in one go.
Nothing touches the real catalog until commit; a commit is **revertible**.

This replaces the slow one-item-at-a-time wizard for onboarding-scale entry.
The wizard stays for day-to-day single adds.

## Non-goals / explicit ceilings

- **AI never gates a commit.** Every draft holds name+qty+price, so it commits
  as typed even when the AI identified nothing (no-name spare parts).
- **No live AI Q&A in the app.** In manual mode the clarification back-and-forth
  happens *inside the chatbot*; the app only parses the final pasted JSON.
- **Web search only where the provider has it** (Gemini grounding). Local/other
  models answer from memory; the owner's review is the backstop.
- Not mobile-specific. It's a normal admin page (served over LAN like any other).

## Data model

New table, migration `plugins/aicatalog/migrations/0005_mock_products.sql`:

```sql
CREATE TABLE aicatalog_mock_products (
    id            BIGSERIAL PRIMARY KEY,       -- the "mock id" used as the batch key
    name          TEXT NOT NULL,
    detail        TEXT NOT NULL DEFAULT '',    -- optional owner explanation ("what it is")
    qty           TEXT NOT NULL DEFAULT '',
    barcode       TEXT NOT NULL DEFAULT '',    -- scanned real barcode only; blank => generate at commit
    cost_price    TEXT NOT NULL DEFAULT '',
    selling_price TEXT NOT NULL DEFAULT '',
    -- enrichment (filled by AI or left as typed)
    category      TEXT NOT NULL DEFAULT '',
    specs         TEXT NOT NULL DEFAULT '',
    explanation   TEXT NOT NULL DEFAULT '',
    confident     BOOLEAN NOT NULL DEFAULT false,
    -- lifecycle
    status              TEXT NOT NULL DEFAULT 'draft',  -- draft | committed
    created_product_id  BIGINT,                          -- set on commit, for revert
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

Prices/qty are stored as text (mirrors `CreateInput` / `AdjustInput`, which take
strings). No FK on `created_product_id` — a reverted/soft-deleted product must
not cascade-break the draft.

## UI — a tab on the existing AI Catalog page

A table with an always-present set of columns:

| Name | Detail (optional) | Qty | Barcode | Cost | Sell | (row status) |

- **Add row** appends a blank editable row; Enter/Tab moves along, so many rows
  go in fast. Rows autosave (HTMX post per row) or save on blur.
- **Barcode:** a scan field (kept verbatim if the item has a real one) or blank
  → "generate at commit".
- **Cost/Sell two-way:** a per-row **cost|sell toggle** + a markup that defaults
  from settings (`DefaultMarkup`) and is overridable per row ("custom"). Typing
  the toggled side fills the other via the shared `costFromMarkup` /
  `sellFromMarkup` helper.
- **Detail** is a free-text column, **optional** — present on every row, filled
  only where the owner wants to explain what the item is.
- Batch actions: **Enrich**, **Commit selected/all**, **Discard**, and (when
  committed rows exist) **Revert last commit**.

## Enrich flow

Builds over the draft rows currently in `draft` status.

**Prompt (new `EnrichPrompt(rows)`):** a system instruction (reuse the spirit of
`systemPrompt`: general/shallow categories, reuse existing categories, verify
online *if web access*, don't guess) plus a body listing each row:

```
mock_id | name | detail
17 | HCH 6200 2RS | deep groove bearing 10mm bore
18 | CT100 clutch cable |
...
EXISTING shop categories (reuse a fitting one verbatim, else propose new):
- ...
Instructions: If you cannot identify a mock_id, ASK me for clarification,
referencing its mock_id and name, and explain what you need. When everything is
clear, reply with a JSON array only:
[{"id":17,"name":"...","category":"Bearings","specs":"...","explanation":"...","confident":true}]
plus a one-line summary. Keep every id I gave you.
```

**API-key mode:** send via `complete(system, body, isGemini())`, parse, apply.
No clarification loop (one shot) — unidentified rows come back
`confident=false` and are flagged for manual fill.

**Manual mode:** render the prompt in a copy box. Owner pastes into a chatbot,
does any Q&A there, copies back **only the JSON array**, pastes it into an
"apply" box.

**Parser (new `parseEnrich([]byte)`):** extract the first top-level JSON array
even if wrapped in summary prose; return `[]EnrichResult{ID,Name,Category,
Specs,Explanation,Confident}`. **Match each result to its draft by `id`.**
Ignore ids that don't exist; leave un-returned drafts untouched (still
committable as typed). Never error the whole batch over one bad entry.

Applying an enrich result updates the draft's `name` (only if the AI gave a
better one and the row wasn't user-locked), `category`, `specs`, `explanation`,
`confident`.

**Categories — feed, then reuse-or-create.** The prompt always includes the
shop's existing category tree (via `CategoryPaths`), and the instruction is:
*reuse a fitting existing path verbatim so similar items land together; propose
a new path only when none fits.* At commit, `FindOrCreateByPath` closes the same
loop on the real side — it **reuses** the category when the path already exists
and **creates** it (nesting on `>`) only when it doesn't. So an AI-reused path
matches an existing category and adds nothing new; an AI-proposed path becomes a
new category exactly once and every later item routed there reuses it.

## Commit (mock → real)

For each selected `draft` row, in order:

1. **Dup guard:** `products.FindByName(name)` — if a real product exists, skip
   with a per-row warning (don't create a duplicate). Also warn on duplicate
   names *within* the selected drafts.
2. Resolve category: `FindOrCreateByPath(category or "Uncategorized")` (normalise
   `/`→`>`, as `Save` already does).
3. Barcode: keep a scanned one if still free, else `GenerateBarcode`.
4. `products.Create(CreateInput{Name, Barcode, CategoryID, UnitID(default),
   CostPrice, SellingPrice})`.
5. If qty positive: `stock.Adjust(NewQuantity: qty, Note: "AI mock commit",
   SellingPrice)`.
6. `LearnItem{ProductID, ResolvedName, SuggestedCategory, Specs,
   UserExplanation: detail, Source, RawQuery: name}`.
7. Mark draft `status='committed'`, set `created_product_id`.

Commit returns a summary toast ("Created N, skipped M duplicates").

## Revert

- **Revert last commit** (or per row): for each committed row — reverse stock
  (mirror intake undo: `Adjust` to `stock - qty`, clamp 0), `products.Delete`
  (soft-disable, recoverable via Show disabled), then set the draft back to
  `status='draft'`, clear `created_product_id`.
- **Discard draft:** delete the mock row outright.
- **Clear committed:** housekeeping to drop committed drafts once the owner is
  happy (products already exist; only the staging rows go).

## Changes to existing plugin code

1. `ai.go`: add `EnrichPrompt`, `parseEnrich`, `EnrichResult`; reuse
   `complete`. Leave `IdentifyPrompt`/`parseIdentify` untouched.
2. Extract two-way price math (`costFromMarkup` + new `sellFromMarkup`) into one
   helper used by both the wizard Price step and the mock table.
3. Add shared `FindByName` dup guard; apply to the wizard `Save` too (fixes the
   existing no-dup-check hole).
4. New `mock.go`: staging store methods + HTTP handlers.
5. `store.go`: mock CRUD, commit, revert helpers.
6. `aicatalog.go`: routes — `GET /mock` (tab), row upsert/delete, `/mock/enrich`,
   `/mock/apply` (paste JSON), `/mock/commit`, `/mock/revert`.
7. `pages.templ`: mock table tab + row template + enrich/apply/commit controls.

## Error handling

- Bad/empty JSON on apply → keep drafts, show "couldn't read that — paste the
  JSON array" (like `IdentifyManual` does today).
- AI down / no key on API enrich → surface the error, drafts stay editable
  (enrich is optional).
- Commit is per-row: one failing row (e.g. create error) is reported and skipped;
  the rest still commit. Duplicates are skipped, not errors.
- Never lose a draft to an AI or parse failure.

## Testing (one runnable check each for non-trivial logic)

- `parseEnrich`: array wrapped in summary prose; missing/extra ids; one
  malformed entry among good ones → good ones survive, bad ignored.
- price helper: cost→sell and sell→cost with a markup; custom override.
- commit dup guard: existing real name skipped; intra-batch dup warned.
- revert: committed product soft-disabled + stock backed out + draft restored.

## Optimize in chunks (large catalogs)

Second, related change to the **existing** category Optimize.

**Problem.** `OptimizeProducts(ctx, 300)` (optimize.go:74) caps at 300 with no
offset, so in a 1000-item shop it always tidies the *same first 300* and the
other ~700 are never optimized. And one prompt with 1000 products is too large
to paste into a chatbot.

**Solution — page the products, keep the whole tree.** Categories are small, so
**every chunk feeds the full current category tree**; only the product list is
paged, `N` per chunk (setting `optimize_chunk_size`, default 150).

Flow, reusing the existing preview→apply machinery per chunk:

- **Manual mode:** step *k* shows the prompt for products `[k·N, (k+1)·N)` + the
  full tree → owner pastes into chatbot → pastes reply → `OptimizePreview` →
  `OptimizeApply` for that chunk → **Next chunk**. A progress badge shows
  "Chunk k of K · products X–Y of T" (same hidden-field pattern the identify
  bulk queue already uses — offset + total in the form).
- **API-key mode:** loop the chunks automatically, applying each, and show a
  combined summary at the end.

**One Revert for the whole session.** Chunk applies would otherwise create K
separate runs (and `Revert` only undoes the latest). So `ApplyPlan` gains an
optional `runID`: the first chunk creates the optimize run, later chunks
**append** their undo ops to that same run (read-modify-write the `undo` JSON
inside the tx) and update its summary. One **Revert** then rolls back the entire
chunked session. Reverse-order replay still holds across appended ops.

**Store changes.** `OptimizeProducts(ctx, limit, offset)` (add offset; callers
updated) + `CountOptimizeProducts(ctx)` for `K = ceil(T/N)`. Everything else
(`OptimizeCategories`, `parseOptimizePlan`, `ApplyPlan` validation, `Revert`)
is reused.

**Safety / known ceilings.**
- Re-feeding the current tree each chunk + `ApplyPlan`'s id validation makes
  cross-chunk id drift a non-issue (unknown ids skipped).
- Structure ops (rename/merge/reparent) may be re-suggested in several chunks;
  they're idempotent or skipped (`old==To`, self/unknown merge, merged-away id
  absent next time), so re-suggestion is harmless.
- Offset paging over `ORDER BY id` is stable because products are never deleted
  by a plan.

**Testing.** Pagination covers every product with no overlap across chunks;
apply chunk 1 (creates run) then chunk 2 (appends) then a single `Revert`
restores the pre-session tree and placements.

## Out of scope (add later if wanted)

- Photos on mock rows.
- Persisting `detail` as a visible product field (currently kept via LearnItem);
  wire to Product Plus later if it needs to show on the product.
- CSV import into the mock table (existing catalog CSV import already covers bulk
  from a file).
