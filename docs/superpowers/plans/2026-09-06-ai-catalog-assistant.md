# AI Catalog Assistant Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A POS plugin that turns "make a product" into a fast conversational stepper — type a part code, let the AI identify it and suggest a category, dup-check/generate a barcode, compute cost from a markup fraction, set qty, print labels — one input at a time.

**Architecture:** New optional plugin `plugins/aicatalog/` mirroring `plugins/clearance/`. It calls an OpenAI-compatible AI endpoint (Gemini free tier by default; OpenRouter etc. via settings) over stdlib `net/http`. It reuses core services for all writes: `products.Service` (create/barcode), `stock.Service` (qty), a plugin-built `categories.Service` (category path), and the existing `/admin/labels/send` HTTP endpoint for printing. It stores what the AI learned per product so a future Phase-2 Optimize never re-asks. Core stays inert without the plugin (compile-time plugin rule).

**Tech Stack:** Go, Echo, templ, HTMX, Alpine.js, Tailwind v3, PostgreSQL, `github.com/shopspring/decimal`, goose migrations. AI via OpenAI-compatible `/chat/completions` JSON.

**Spec:** `docs/superpowers/specs/2026-09-06-ai-catalog-assistant-design.md`

## Global Constraints

- **Plugin isolation:** core must build and run inert with no plugin imported. Never import `internal/web` from the plugin. Committed `cmd/server/enabled_plugins.go` stays core-only; enable the plugin for testing via a **local, uncommitted** import.
- **No new Go dependency.** AI client uses stdlib `net/http` + `encoding/json` only.
- **API key server-side only** — never rendered into any template or JS.
- **AI never writes to the catalog silently** — every product/category write happens only on the explicit Save step after the user has seen the values.
- **Money uses `decimal.Decimal` + `internal/money`** (there is no `money.Decimal`; import `github.com/shopspring/decimal`).
- **Scanner Enter gotcha:** the barcode input is the last field in its step; its Enter must trigger the barcode action, not skip ahead.
- **Commit trailer on every commit:**
  ```
  Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
  Claude-Session: https://claude.ai/code/session_01EdqF8cvGdw27z5jmRBXTTg
  ```
- **Build/verify commands** (scratchpad binary path):
  - templ: `templ generate ./plugins/aicatalog/`
  - build: `go build -o /tmp/claude-1000/-home-karots-Projects-go-karots-pos/eca63fae-e54c-4c83-9e74-8219f3bf99f7/scratchpad/pos-server ./cmd/server`
  - check exit: run build then `echo "exit=$?"` (do NOT pipe through `tail`/`&& echo OK` — it masks the real exit code).
  - run: `pkill -f 'scratchpad/pos-server'` (run separately — it returns 144 and aborts `&&` chains), then `bash -c 'set -a && . ./.env && set +a && <binary>'`. Always pkill before restart (port 3000 stays held otherwise).
  - login for manual test: phone `0771234567`, pin `1234` (admin uid 1).
  - DB: `docker exec pos_db psql -U pos_user -d pos_db -tAc "..."`.

## File Structure

```
plugins/aicatalog/
  plugin.json          # key/name/import/version/description (bootstrapper discovery)
  aicatalog.go         # Plugin{} + init()/Register + Name/Migrations/Setup (routes, nav, settings section, detail row)
  ai.go                # OpenAI-compatible client: Client, Identify() -> IdentifyResult; JSON request/response structs
  store.go             # Store: settings get/save, item learn/get; pure helpers costFromMarkup, parseIdentify; IdentifyResult/Candidate types live here
  admin.go             # adminUI handlers: Page, Identify, Pick, Category, Barcode, Price, Qty, Save, SaveSettings, TestKey
  pages.templ          # Page shell + one step-card component per step + settings form
  migrations/
    embed.go           # //go:embed *.sql  -> var FS embed.FS
    0001_init.sql      # aicatalog_settings + aicatalog_items
  store_test.go        # TDD: costFromMarkup, parseIdentify
  ai_test.go           # Identify against an httptest server
```

No core files are modified. The plugin builds its own `categories.Service` from `core.DB`.

---

### Task 1: Store, pure helpers, and migration

**Files:**
- Create: `plugins/aicatalog/store.go`
- Create: `plugins/aicatalog/migrations/0001_init.sql`
- Create: `plugins/aicatalog/migrations/embed.go`
- Test: `plugins/aicatalog/store_test.go`

**Interfaces:**
- Produces:
  - `type Settings struct { Provider, BaseURL, Model, APIKey string; DefaultMarkup decimal.Decimal }`
  - `type Candidate struct { Name, Category, Specs, Explanation string }`
  - `type IdentifyResult struct { Confident bool; Best Candidate; Options []Candidate }`
  - `type LearnedItem struct { ProductID int64; ResolvedName, SuggestedCategory, Specs, UserExplanation, Source, RawQuery string }`
  - `func NewStore(db *sqlx.DB) *Store`
  - `func (s *Store) GetSettings(ctx) (Settings, error)`
  - `func (s *Store) SaveSettings(ctx, Settings) error`
  - `func (s *Store) LearnItem(ctx, LearnedItem) error`
  - `func (s *Store) GetItem(ctx, productID int64) (*LearnedItem, error)`
  - `func costFromMarkup(selling, markup decimal.Decimal) (decimal.Decimal, error)`
  - `func parseIdentify(raw []byte) (IdentifyResult, error)`

- [ ] **Step 1: Write the migration**

`plugins/aicatalog/migrations/0001_init.sql`:
```sql
-- +goose Up
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
  source             TEXT NOT NULL,
  raw_query          TEXT NOT NULL DEFAULT '',
  created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at         TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE aicatalog_items;
DROP TABLE aicatalog_settings;
```

- [ ] **Step 2: Write the embed file**

`plugins/aicatalog/migrations/embed.go`:
```go
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
```

- [ ] **Step 3: Write the failing tests**

`plugins/aicatalog/store_test.go`:
```go
package aicatalog

import (
	"testing"

	"github.com/shopspring/decimal"
)

func TestCostFromMarkup(t *testing.T) {
	// 500 selling / 1.4 markup = 357.14 -> rounded whole = 357
	got, err := costFromMarkup(decimal.RequireFromString("500"), decimal.RequireFromString("1.4"))
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !got.Equal(decimal.RequireFromString("357")) {
		t.Fatalf("want 357, got %s", got)
	}
}

func TestCostFromMarkupRejectsMarkupLEOne(t *testing.T) {
	if _, err := costFromMarkup(decimal.RequireFromString("500"), decimal.RequireFromString("1")); err == nil {
		t.Fatal("markup <= 1 should error")
	}
	if _, err := costFromMarkup(decimal.RequireFromString("500"), decimal.Zero); err == nil {
		t.Fatal("markup 0 should error (no divide by zero)")
	}
}

func TestParseIdentifyConfident(t *testing.T) {
	raw := []byte(`{"confident":true,"best":{"name":"Deep Groove Ball Bearing 6200 2RS","category":"Bearings/Deep Groove","specs":"10x30x9mm sealed","explanation":"Sealed radial bearing"},"options":[]}`)
	got, err := parseIdentify(raw)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !got.Confident || got.Best.Category != "Bearings/Deep Groove" {
		t.Fatalf("bad parse: %+v", got)
	}
}

func TestParseIdentifyStripsCodeFence(t *testing.T) {
	raw := []byte("```json\n{\"confident\":false,\"best\":{},\"options\":[{\"name\":\"A\",\"category\":\"X\"},{\"name\":\"B\",\"category\":\"Y\"}]}\n```")
	got, err := parseIdentify(raw)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got.Confident || len(got.Options) != 2 {
		t.Fatalf("bad parse: %+v", got)
	}
}

func TestParseIdentifyGarbageErrors(t *testing.T) {
	if _, err := parseIdentify([]byte("not json at all")); err == nil {
		t.Fatal("garbage should error, not panic")
	}
}
```

- [ ] **Step 4: Run tests to verify they fail**

Run: `go test ./plugins/aicatalog/ -run 'CostFromMarkup|ParseIdentify' -v`
Expected: FAIL (undefined: costFromMarkup, parseIdentify, Store, types).

- [ ] **Step 5: Write store.go**

`plugins/aicatalog/store.go`:
```go
package aicatalog

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"github.com/jmoiron/sqlx"
	"github.com/shopspring/decimal"
)

type Store struct{ db *sqlx.DB }

func NewStore(db *sqlx.DB) *Store { return &Store{db: db} }

type Settings struct {
	Provider      string          `db:"provider"`
	BaseURL       string          `db:"base_url"`
	Model         string          `db:"model"`
	APIKey        string          `db:"api_key"`
	DefaultMarkup decimal.Decimal `db:"default_markup"`
}

type Candidate struct {
	Name        string `json:"name"`
	Category    string `json:"category"`
	Specs       string `json:"specs"`
	Explanation string `json:"explanation"`
}

type IdentifyResult struct {
	Confident bool        `json:"confident"`
	Best      Candidate   `json:"best"`
	Options   []Candidate `json:"options"`
}

type LearnedItem struct {
	ProductID         int64  `db:"product_id"`
	ResolvedName      string `db:"resolved_name"`
	SuggestedCategory string `db:"suggested_category"`
	Specs             string `db:"specs"`
	UserExplanation   string `db:"user_explanation"`
	Source            string `db:"source"`
	RawQuery          string `db:"raw_query"`
}

func (s *Store) GetSettings(ctx context.Context) (Settings, error) {
	var out Settings
	err := s.db.GetContext(ctx, &out,
		`SELECT provider, base_url, model, api_key, default_markup FROM aicatalog_settings WHERE id = 1`)
	return out, err
}

func (s *Store) SaveSettings(ctx context.Context, in Settings) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE aicatalog_settings SET provider=$1, base_url=$2, model=$3, api_key=$4, default_markup=$5 WHERE id = 1`,
		in.Provider, in.BaseURL, in.Model, in.APIKey, in.DefaultMarkup)
	return err
}

func (s *Store) LearnItem(ctx context.Context, it LearnedItem) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO aicatalog_items
		  (product_id, resolved_name, suggested_category, specs, user_explanation, source, raw_query, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7, now())
		ON CONFLICT (product_id) DO UPDATE SET
		  resolved_name=$2, suggested_category=$3, specs=$4,
		  user_explanation=$5, source=$6, raw_query=$7, updated_at=now()`,
		it.ProductID, it.ResolvedName, it.SuggestedCategory, it.Specs,
		it.UserExplanation, it.Source, it.RawQuery)
	return err
}

func (s *Store) GetItem(ctx context.Context, productID int64) (*LearnedItem, error) {
	var out LearnedItem
	err := s.db.GetContext(ctx, &out, `
		SELECT product_id, resolved_name, suggested_category, specs,
		       user_explanation, source, raw_query
		FROM aicatalog_items WHERE product_id = $1`, productID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// costFromMarkup derives cost from a selling price and a markup multiple:
// cost = round(selling / markup) to a whole number. markup must be > 1.
func costFromMarkup(selling, markup decimal.Decimal) (decimal.Decimal, error) {
	if markup.LessThanOrEqual(decimal.NewFromInt(1)) {
		return decimal.Zero, errors.New("markup must be greater than 1")
	}
	return selling.Div(markup).Round(0), nil
}

// parseIdentify tolerantly parses the model's reply into an IdentifyResult:
// it strips a leading ```json fence / trailing ``` and any prose around the
// outermost JSON object before unmarshalling.
func parseIdentify(raw []byte) (IdentifyResult, error) {
	var out IdentifyResult
	s := strings.TrimSpace(string(raw))
	if i := strings.IndexByte(s, '{'); i >= 0 {
		if j := strings.LastIndexByte(s, '}'); j >= i {
			s = s[i : j+1]
		}
	}
	dec := json.NewDecoder(bytes.NewReader([]byte(s)))
	if err := dec.Decode(&out); err != nil {
		return IdentifyResult{}, err
	}
	return out, nil
}
```

- [ ] **Step 6: Run tests to verify they pass**

Run: `go test ./plugins/aicatalog/ -run 'CostFromMarkup|ParseIdentify' -v`
Expected: PASS (5 tests).

- [ ] **Step 7: Commit**

```bash
git add plugins/aicatalog/store.go plugins/aicatalog/store_test.go plugins/aicatalog/migrations/
git commit -m "feat(aicatalog): store, markup/parse helpers, migration"
```

---

### Task 2: AI client (`ai.go`)

**Files:**
- Create: `plugins/aicatalog/ai.go`
- Test: `plugins/aicatalog/ai_test.go`

**Interfaces:**
- Consumes (Task 1): `Settings`, `IdentifyResult`, `parseIdentify`.
- Produces:
  - `type Client struct { cfg Settings; hc *http.Client }`
  - `func NewClient(cfg Settings) *Client`
  - `func (c *Client) Identify(ctx context.Context, query, userHint string) (IdentifyResult, error)`
  - `func (c *Client) Ping(ctx context.Context) error` (tiny call for the Test-key button)

- [ ] **Step 1: Write the failing test**

`plugins/aicatalog/ai_test.go`:
```go
package aicatalog

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestIdentifyParsesChatCompletion(t *testing.T) {
	// Fake OpenAI-compatible server that echoes a canned identify JSON as the
	// assistant message content.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/chat/completions") {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("missing bearer, got %q", got)
		}
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), "6200 2rs") {
			t.Errorf("query not forwarded: %s", body)
		}
		content := `{"confident":true,"best":{"name":"Ball Bearing 6200 2RS","category":"Bearings/Deep Groove","specs":"10x30x9","explanation":"sealed"},"options":[]}`
		resp := map[string]any{"choices": []map[string]any{{"message": map[string]any{"content": content}}}}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	c := NewClient(Settings{BaseURL: srv.URL, Model: "x", APIKey: "test-key"})
	got, err := c.Identify(context.Background(), "6200 2rs", "")
	if err != nil {
		t.Fatalf("Identify err: %v", err)
	}
	if !got.Confident || got.Best.Category != "Bearings/Deep Groove" {
		t.Fatalf("bad result: %+v", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./plugins/aicatalog/ -run TestIdentify -v`
Expected: FAIL (undefined: NewClient, Identify).

- [ ] **Step 3: Write ai.go**

`plugins/aicatalog/ai.go`:
```go
package aicatalog

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type Client struct {
	cfg Settings
	hc  *http.Client
}

func NewClient(cfg Settings) *Client {
	return &Client{cfg: cfg, hc: &http.Client{Timeout: 20 * time.Second}}
}

const systemPrompt = `You identify retail/spare-part products from a short code or name.
Reply with STRICT JSON ONLY, no prose, matching:
{"confident":bool,"best":{"name":"","category":"","specs":"","explanation":""},
"options":[{"name":"","category":"","specs":"","explanation":""}]}
Set confident=true and fill "best" only when you are sure. Otherwise set
confident=false and give up to 4 "options". "category" is a slash path suitable
for a shop, e.g. "Bearings/Deep Groove". "explanation" is one short sentence a
non-expert understands. "specs" holds key dimensions/ratings if known.`

type chatReq struct {
	Model    string    `json:"model"`
	Messages []chatMsg `json:"messages"`
}
type chatMsg struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}
type chatResp struct {
	Choices []struct {
		Message chatMsg `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func (c *Client) call(ctx context.Context, messages []chatMsg) (string, error) {
	if strings.TrimSpace(c.cfg.APIKey) == "" {
		return "", errors.New("AI API key is not set — add it in AI Catalog settings")
	}
	payload, _ := json.Marshal(chatReq{Model: c.cfg.Model, Messages: messages})
	url := strings.TrimRight(c.cfg.BaseURL, "/") + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
	resp, err := c.hc.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var cr chatResp
	if err := json.Unmarshal(body, &cr); err != nil {
		return "", fmt.Errorf("AI returned unreadable response (HTTP %d)", resp.StatusCode)
	}
	if cr.Error != nil && cr.Error.Message != "" {
		return "", errors.New(cr.Error.Message)
	}
	if resp.StatusCode >= 400 || len(cr.Choices) == 0 {
		return "", fmt.Errorf("AI request failed (HTTP %d)", resp.StatusCode)
	}
	return cr.Choices[0].Message.Content, nil
}

func (c *Client) Identify(ctx context.Context, query, userHint string) (IdentifyResult, error) {
	user := "Identify this item: " + query
	if strings.TrimSpace(userHint) != "" {
		user += "\nAdditional context from the shop owner: " + userHint
	}
	content, err := c.call(ctx, []chatMsg{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: user},
	})
	if err != nil {
		return IdentifyResult{}, err
	}
	return parseIdentify([]byte(content))
}

func (c *Client) Ping(ctx context.Context) error {
	_, err := c.call(ctx, []chatMsg{{Role: "user", Content: "reply with the single word: ok"}})
	return err
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./plugins/aicatalog/ -run TestIdentify -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add plugins/aicatalog/ai.go plugins/aicatalog/ai_test.go
git commit -m "feat(aicatalog): OpenAI-compatible AI identify client"
```

---

### Task 3: Plugin registration + settings page (compiles, migration runs, inert page)

**Files:**
- Create: `plugins/aicatalog/plugin.json`
- Create: `plugins/aicatalog/aicatalog.go`
- Create: `plugins/aicatalog/admin.go` (settings + a placeholder Page for now)
- Create: `plugins/aicatalog/pages.templ` (settings form + empty stepper shell)

**Interfaces:**
- Consumes: `NewStore`, `NewClient`, `GetSettings/SaveSettings`, core services on `plugin.Core` (`Products`, `Stock`, `DB`, `Settings`).
- Produces: routes under `/admin/ai-catalog`, admin nav entry, `adminUI` handler type.

- [ ] **Step 1: Write plugin.json**

```json
{
  "key": "aicatalog",
  "name": "AI Catalog",
  "import": "karots-pos/plugins/aicatalog",
  "version": "1.0.0",
  "description": "AI-assisted fast product entry: identify a part from its code, suggest a category, dup-check/generate a barcode, cost-from-markup, set qty, print labels."
}
```

- [ ] **Step 2: Write aicatalog.go**

```go
// Package aicatalog is an AI-assisted fast product-entry plugin. Core never
// imports it; it attaches through generic plugin hooks and reuses core product,
// stock and category services for all writes. Depends on no other plugin.
package aicatalog

import (
	"context"
	"io/fs"

	"karots-pos/internal/features/categories"
	"karots-pos/internal/plugin"
	"karots-pos/plugins/aicatalog/migrations"
)

func init() { plugin.Register(&Plugin{}) }

type Plugin struct {
	core  plugin.Core
	store *Store
	cats  *categories.Service
}

func (p *Plugin) Name() string                { return "AI Catalog" }
func (p *Plugin) Migrations() (fs.FS, string) { return migrations.FS, "aicatalog" }

func (p *Plugin) Setup(reg *plugin.Registry) {
	p.core = reg.Core
	p.store = NewStore(reg.Core.DB)
	p.cats = categories.NewService(reg.Core.DB)

	a := &adminUI{p: p}
	reg.Admin().GET("/ai-catalog", a.Page)
	reg.Admin().POST("/ai-catalog/settings", a.SaveSettings)
	reg.Admin().POST("/ai-catalog/test-key", a.TestKey)
	reg.Admin().POST("/ai-catalog/identify", a.Identify)
	reg.Admin().POST("/ai-catalog/pick", a.Pick)
	reg.Admin().POST("/ai-catalog/category", a.Category)
	reg.Admin().POST("/ai-catalog/barcode", a.Barcode)
	reg.Admin().POST("/ai-catalog/price", a.Price)
	reg.Admin().POST("/ai-catalog/qty", a.Qty)
	reg.Admin().POST("/ai-catalog/save", a.Save)

	reg.AddAdminNav(plugin.AdminNavEntry{
		SectionLabel: "AI Catalog", Icon: "🤖",
		Href: "/admin/ai-catalog", Label: "Add products", Key: "aicatalog",
		Desc: "AI-assisted fast product entry",
	})

	// Info-popup row: show what the AI decided this product is.
	reg.AddProductDetailContributor(plugin.ProductDetailContributor{
		Rows: func(ctx context.Context, id int64) ([]plugin.DetailRow, error) {
			it, err := p.store.GetItem(ctx, id)
			if err != nil || it == nil || it.ResolvedName == "" {
				return nil, err
			}
			return []plugin.DetailRow{{Label: "AI identified", Value: it.ResolvedName}}, nil
		},
	})
}
```

> NOTE: `ProductDetailContributor` and `DetailRow` are the same types clearance uses (`internal/plugin/hooks.go`). If the `Rows` field signature differs at build time, match it to clearance's usage in `plugins/clearance/clearance.go`.

- [ ] **Step 3: Write admin.go (settings + placeholder Page/handlers)**

Write `adminUI` with `Page`, `SaveSettings`, `TestKey`, and **stub** the stepper handlers (`Identify`, `Pick`, `Category`, `Barcode`, `Price`, `Qty`, `Save`) to `return response.NoContent(c)` for now — they are filled in Tasks 4–5. `Page` renders the settings form + step-1 shell.

```go
package aicatalog

import (
	"net/http"
	"strconv"

	"karots-pos/internal/middleware"
	"karots-pos/internal/response"

	"github.com/labstack/echo/v4"
	"github.com/shopspring/decimal"
)

type adminUI struct{ p *Plugin }

func (a *adminUI) Page(c echo.Context) error {
	ctx := c.Request().Context()
	cfg, err := a.p.store.GetSettings(ctx)
	if err != nil {
		return err
	}
	return response.RenderPage(c, Page(PageData{
		UserName:      middleware.CurrentUserName(c),
		Provider:      cfg.Provider,
		BaseURL:       cfg.BaseURL,
		Model:         cfg.Model,
		HasKey:        cfg.APIKey != "",
		DefaultMarkup: cfg.DefaultMarkup.String(),
	}))
}

func (a *adminUI) SaveSettings(c echo.Context) error {
	ctx := c.Request().Context()
	cur, err := a.p.store.GetSettings(ctx)
	if err != nil {
		return err
	}
	cur.Provider = c.FormValue("provider")
	cur.BaseURL = c.FormValue("base_url")
	cur.Model = c.FormValue("model")
	// Only overwrite the key when a new one is typed (form shows a placeholder,
	// never the stored key), so saving other fields doesn't wipe it.
	if k := c.FormValue("api_key"); k != "" {
		cur.APIKey = k
	}
	if m, e := decimal.NewFromString(c.FormValue("default_markup")); e == nil && m.GreaterThan(decimal.NewFromInt(1)) {
		cur.DefaultMarkup = m
	}
	if err := a.p.store.SaveSettings(ctx, cur); err != nil {
		return err
	}
	return c.Redirect(http.StatusSeeOther, "/admin/ai-catalog")
}

func (a *adminUI) TestKey(c echo.Context) error {
	cfg, err := a.p.store.GetSettings(c.Request().Context())
	if err != nil {
		return err
	}
	if err := NewClient(cfg).Ping(c.Request().Context()); err != nil {
		c.Response().Header().Set("HX-Trigger", response.Toast("AI test failed: "+err.Error(), "error"))
		return response.NoContent(c)
	}
	c.Response().Header().Set("HX-Trigger", response.Toast("AI key works ✓", "success"))
	return response.NoContent(c)
}

// --- stepper stubs, implemented in later tasks ---
func (a *adminUI) Identify(c echo.Context) error { return response.NoContent(c) }
func (a *adminUI) Pick(c echo.Context) error     { return response.NoContent(c) }
func (a *adminUI) Category(c echo.Context) error { return response.NoContent(c) }
func (a *adminUI) Barcode(c echo.Context) error  { return response.NoContent(c) }
func (a *adminUI) Price(c echo.Context) error    { return response.NoContent(c) }
func (a *adminUI) Qty(c echo.Context) error      { return response.NoContent(c) }
func (a *adminUI) Save(c echo.Context) error     { return response.NoContent(c) }

var _ = strconv.Itoa // remove once strconv is used in later tasks
```

- [ ] **Step 4: Write pages.templ (settings form + step-1 shell)**

`plugins/aicatalog/pages.templ` — a `PageData` struct and a `Page` templ wrapping `layouts.Admin("AI Catalog", d.UserName, "aicatalog")`. Include:
- A **settings** panel (collapsible `<details>`): form `POST /admin/ai-catalog/settings` with inputs `provider`, `base_url`, `model`, `api_key` (type=password, `placeholder={ "•••• saved" if d.HasKey else "paste key" }`, never pre-filled), `default_markup`; plus a **Test key** button `hx-post="/admin/ai-catalog/test-key"`.
- A **stepper** container `<div id="ai-step">` holding step 1: a form `hx-post="/admin/ai-catalog/identify"` `hx-target="#ai-step"` with a single autofocused text input `name="query"` ("Type a name or part number, e.g. 6200 2RS") and an Identify button.

```go
package aicatalog

import "karots-pos/templates/layouts"

type PageData struct {
	UserName      string
	Provider      string
	BaseURL       string
	Model         string
	HasKey        bool
	DefaultMarkup string
}

templ Page(d PageData) {
	@layouts.Admin("AI Catalog", d.UserName, "aicatalog") {
		<div class="max-w-2xl mx-auto">
			<h1 class="text-2xl font-bold mb-1">AI Catalog — add products fast</h1>
			<p class="text-sm text-slate-500 mb-4">Type a part code; the AI names it, suggests a category, and walks you through one step at a time.</p>
			<details class="bg-white rounded-2xl shadow-sm p-4 mb-6">
				<summary class="cursor-pointer text-sm font-medium">AI settings</summary>
				<form method="post" action="/admin/ai-catalog/settings" class="mt-4 grid gap-3">
					<label class="text-xs text-slate-500">Provider
						<input name="provider" value={ d.Provider } class="mt-1 w-full border rounded-lg px-3 py-1.5"/>
					</label>
					<label class="text-xs text-slate-500">Base URL
						<input name="base_url" value={ d.BaseURL } class="mt-1 w-full border rounded-lg px-3 py-1.5"/>
					</label>
					<label class="text-xs text-slate-500">Model
						<input name="model" value={ d.Model } class="mt-1 w-full border rounded-lg px-3 py-1.5"/>
					</label>
					<label class="text-xs text-slate-500">API key
						if d.HasKey {
							<input name="api_key" type="password" placeholder="•••• saved (leave blank to keep)" class="mt-1 w-full border rounded-lg px-3 py-1.5"/>
						} else {
							<input name="api_key" type="password" placeholder="paste your key" class="mt-1 w-full border rounded-lg px-3 py-1.5"/>
						}
					</label>
					<label class="text-xs text-slate-500">Default markup (selling ÷ this = cost)
						<input name="default_markup" value={ d.DefaultMarkup } class="mt-1 w-32 border rounded-lg px-3 py-1.5"/>
					</label>
					<div class="flex gap-2">
						<button class="px-4 py-1.5 rounded-lg bg-indigo-600 text-white text-sm font-medium">Save</button>
						<button type="button" hx-post="/admin/ai-catalog/test-key" class="px-4 py-1.5 rounded-lg border text-sm">Test key</button>
					</div>
				</form>
			</details>
			<div id="ai-step" class="bg-white rounded-2xl shadow-sm p-6">
				@StepIdentify()
			</div>
		</div>
	}
}

templ StepIdentify() {
	<form hx-post="/admin/ai-catalog/identify" hx-target="#ai-step" hx-swap="innerHTML">
		<label class="block text-sm font-medium mb-2">What is it? (name or part number)</label>
		<input name="query" autofocus placeholder="e.g. 6200 2RS" class="w-full border rounded-lg px-3 py-2 text-lg"/>
		<button class="mt-3 px-5 py-2 rounded-lg bg-indigo-600 text-white font-medium">Identify →</button>
	</form>
}
```

- [ ] **Step 5: Enable the plugin locally (uncommitted) and build**

Create `cmd/server/enabled_plugins_local.go` (this file is dev-only; do NOT commit it):
```go
package main

import _ "karots-pos/plugins/aicatalog"
```
Then:
```
templ generate ./plugins/aicatalog/
go build -o /tmp/claude-1000/-home-karots-Projects-go-karots-pos/eca63fae-e54c-4c83-9e74-8219f3bf99f7/scratchpad/pos-server ./cmd/server
echo "exit=$?"
```
Expected: `exit=0`.

- [ ] **Step 6: Verify migration runs + page loads**

Run the server (pkill first, then start with `.env`). Then:
```
docker exec pos_db psql -U pos_user -d pos_db -tAc "SELECT provider, base_url FROM aicatalog_settings;"
```
Expected: one row (`gemini | https://...`). Navigate (Playwright, logged in) to `/admin/ai-catalog` → page renders with settings panel + the "What is it?" box. Save settings with a real key, click **Test key** → success or a clear error toast.

- [ ] **Step 7: Commit** (core-only build check first)

```bash
rm cmd/server/enabled_plugins_local.go   # confirm core still builds without the plugin
go build ./cmd/server && echo "core exit=$?"
git add plugins/aicatalog/plugin.json plugins/aicatalog/aicatalog.go plugins/aicatalog/admin.go plugins/aicatalog/pages.templ
git commit -m "feat(aicatalog): plugin registration, settings page, AI key test"
# recreate the local enable file to keep testing
printf 'package main\n\nimport _ "karots-pos/plugins/aicatalog"\n' > cmd/server/enabled_plugins_local.go
```
(Generated `*_templ.go` are gitignored — don't add them.)

---

### Task 4: Stepper — identify → pick → category

**Files:**
- Modify: `plugins/aicatalog/admin.go` (fill `Identify`, `Pick`, `Category`)
- Modify: `plugins/aicatalog/pages.templ` (add `StepOptions`, `StepCategory` components)

**Interfaces:**
- Consumes: `Client.Identify`, `store.GetSettings`, `p.cats.FindOrCreateByPath` (used at Save, previewed here).
- Wizard state travels as hidden form fields, accumulated each step: `query`, `name`, `category`, `specs`, `explanation`, `source`.
- Produces: `Identify` renders `StepOptions`; `Pick` renders `StepCategory`; `Category` renders `StepBarcode` (Task 5).

- [ ] **Step 1: Implement Identify**

```go
func (a *adminUI) Identify(c echo.Context) error {
	ctx := c.Request().Context()
	query := strings.TrimSpace(c.FormValue("query"))
	if query == "" {
		return response.RenderFragment(c, StepIdentifyError("Type a name or part number."))
	}
	cfg, err := a.p.store.GetSettings(ctx)
	if err != nil {
		return err
	}
	res, aerr := NewClient(cfg).Identify(ctx, query, c.FormValue("hint"))
	if aerr != nil {
		// AI down or no key: fall through to manual entry with the raw query as name.
		return response.RenderFragment(c, StepOptions(OptionsData{
			Query:     query,
			AIError:   aerr.Error(),
			Options:   nil,
		}))
	}
	data := OptionsData{Query: query}
	if res.Confident {
		data.Options = []Candidate{res.Best}
		data.Confident = true
	} else {
		data.Options = res.Options
	}
	return response.RenderFragment(c, StepOptions(data))
}
```

- [ ] **Step 2: Implement Pick**

`Pick` reads the chosen option index (or the "explain" free text) from the form, resolves the chosen `Candidate` (posted back as hidden fields `name`,`category`,`specs`,`explanation`), sets `source` (`ai_confident` if the confident flag was set, else `user_picked`; `user_explained` when the explain box was used and no option chosen), and renders `StepCategory` pre-filled with the chosen category.

```go
func (a *adminUI) Pick(c echo.Context) error {
	d := gatherState(c) // helper below
	// "explain" overrides: the owner told us what it is.
	if ex := strings.TrimSpace(c.FormValue("explain")); ex != "" && c.FormValue("name") == "" {
		d.Name = ex
		d.Explanation = ex
		d.Source = "user_explained"
	}
	if d.Name == "" {
		d.Name = d.Query
	}
	return response.RenderFragment(c, StepCategory(d))
}
```

- [ ] **Step 3: Add the state helper**

```go
type stepState struct {
	Query, Name, Category, Specs, Explanation, Source string
}

func gatherState(c echo.Context) stepState {
	s := stepState{
		Query:       strings.TrimSpace(c.FormValue("query")),
		Name:        strings.TrimSpace(c.FormValue("name")),
		Category:    strings.TrimSpace(c.FormValue("category")),
		Specs:       strings.TrimSpace(c.FormValue("specs")),
		Explanation: strings.TrimSpace(c.FormValue("explanation")),
		Source:      strings.TrimSpace(c.FormValue("source")),
	}
	if s.Source == "" {
		s.Source = "user_picked"
	}
	return s
}
```

- [ ] **Step 4: Implement Category (accept/edit → go to barcode)**

```go
func (a *adminUI) Category(c echo.Context) error {
	d := gatherState(c)
	if v := strings.TrimSpace(c.FormValue("category")); v != "" {
		d.Category = v
	}
	return response.RenderFragment(c, StepBarcode(d))
}
```

- [ ] **Step 5: Add templ components**

Add to `pages.templ`:
- `StepIdentifyError(msg string)` — the identify form again with an error line.
- `OptionsData struct { Query, AIError string; Confident bool; Options []Candidate }`.
- `StepOptions(d OptionsData)` — renders each option as a pickable card (a `<form hx-post="/admin/ai-catalog/pick" hx-target="#ai-step">` per option, carrying hidden `query`,`name`,`category`,`specs`,`explanation`,`source`, and a submit button styled as a chip showing `name` + `explanation` + a category tag). When `Confident`, highlight the single card ("Best match"). Always append a **"None — let me explain"** card: a form with a text input `name="explain"` + hidden `query`. When `AIError != ""`, show an amber note and still offer the explain card + a "type it manually" card that posts `name=query`.
- `StepCategory(d stepState)` — shows the suggested category as a pre-filled input `name="category"` (value `d.Category`) with a short helper ("edit the path, e.g. Bearings/Deep Groove"), carries all state as hidden fields, posts to `/admin/ai-catalog/category` with a "Next: barcode →" button.

Each card's chip is a `<button>`; the whole card is the submit so it's one tap. Keep hidden fields consistent (`query,name,category,specs,explanation,source`).

- [ ] **Step 6: templ generate + build**

```
templ generate ./plugins/aicatalog/
go build -o .../scratchpad/pos-server ./cmd/server ; echo "exit=$?"
```
Expected: `exit=0`.

- [ ] **Step 7: Verify (Playwright, real AI key)**

Restart server. On `/admin/ai-catalog`: type `6200 2rs` → Identify. Expect either a highlighted best match or up-to-4 option cards, each explaining the item + a category. Pick one → the Category step shows the suggested path pre-filled. Click Next → the Barcode step appears (Task 5 stub renders). Also test **"None — let me explain"** → type "small sealed bearing" → proceeds with that as the name.

- [ ] **Step 8: Commit**

```bash
git add plugins/aicatalog/admin.go plugins/aicatalog/pages.templ
git commit -m "feat(aicatalog): identify + option-pick + category steps"
```

---

### Task 5: Stepper — barcode → price → qty → labels → save

**Files:**
- Modify: `plugins/aicatalog/admin.go` (fill `Barcode`, `Price`, `Qty`, `Save`)
- Modify: `plugins/aicatalog/pages.templ` (`StepBarcode`, `StepPrice`, `StepQty`, `StepDone`)

**Interfaces:**
- Consumes: `p.core.Products` (`GetByBarcode`, `GenerateBarcode`, `Create`), `p.core.Stock` (`Adjust`), `p.cats.FindOrCreateByPath`, `store.LearnItem`, `costFromMarkup`.
- Wizard state now also carries: `barcode`, `cost`, `qty`, `labels`.

- [ ] **Step 1: Implement Barcode (scan or generate, dup-check)**

```go
func (a *adminUI) Barcode(c echo.Context) error {
	ctx := c.Request().Context()
	d := gatherState(c)
	code := strings.TrimSpace(c.FormValue("barcode"))
	action := c.FormValue("action") // "scan" | "generate"
	var warn string
	switch {
	case action == "generate" || code == "":
		gen, err := a.p.core.Products.GenerateBarcode(ctx)
		if err != nil {
			return err
		}
		code = gen
	default:
		// dup-check a typed/scanned code
		if _, err := a.p.core.Products.GetByBarcode(ctx, code); err == nil {
			warn = "That barcode is already used by another product."
			code = "" // force a choice: rescan or generate
		}
	}
	return response.RenderFragment(c, StepPrice(priceData(d, code, warn), a.markup(ctx)))
}
```
Add helpers:
```go
func (a *adminUI) markup(ctx context.Context) string {
	if cfg, err := a.p.store.GetSettings(ctx); err == nil {
		return cfg.DefaultMarkup.String()
	}
	return "1.4"
}
```
(`priceData` bundles `stepState` + `barcode` + `warn` for the templ; define it in pages.templ or admin.go as a small struct.)

- [ ] **Step 2: Implement Price (cost direct or selling+markup)**

```go
func (a *adminUI) Price(c echo.Context) error {
	d := gatherState(c)
	barcode := strings.TrimSpace(c.FormValue("barcode"))
	cost := strings.TrimSpace(c.FormValue("cost"))
	selling := strings.TrimSpace(c.FormValue("selling"))
	// If cost is blank but selling+markup given, derive cost.
	if cost == "" && selling != "" {
		sell, e1 := decimal.NewFromString(selling)
		mk, e2 := decimal.NewFromString(strings.TrimSpace(c.FormValue("markup")))
		if e1 == nil && e2 == nil {
			if v, err := costFromMarkup(sell, mk); err == nil {
				cost = v.String()
			}
		}
	}
	return response.RenderFragment(c, StepQty(qtyData(d, barcode, cost, selling)))
}
```

- [ ] **Step 3: Implement Qty (→ labels step)**

```go
func (a *adminUI) Qty(c echo.Context) error {
	d := gatherState(c)
	return response.RenderFragment(c, StepLabels(labelsData(c, d)))
}
```
`StepLabels` shows preset chips for label count (= qty / a few (e.g. 5) / custom / none) and a final **Save** button posting to `/admin/ai-catalog/save` with every hidden field: `name,category,specs,explanation,source,barcode,cost,selling,qty,labels`.

- [ ] **Step 4: Implement Save (the one place that writes)**

```go
func (a *adminUI) Save(c echo.Context) error {
	ctx := c.Request().Context()
	d := gatherState(c)
	if d.Name == "" {
		return response.RenderFragment(c, StepIdentifyError("Nothing to save — start again."))
	}
	catID, err := a.p.cats.FindOrCreateByPath(ctx, d.Category)
	if err != nil || catID <= 0 {
		// fall back to Uncategorized path if the AI gave a bad category
		catID, err = a.p.cats.FindOrCreateByPath(ctx, "Uncategorized")
		if err != nil {
			return err
		}
	}
	unitID, err := a.defaultUnitID(ctx)
	if err != nil {
		return err
	}
	barcode := strings.TrimSpace(c.FormValue("barcode"))
	// Final dup safety: never assign a taken barcode.
	if barcode != "" {
		if _, e := a.p.core.Products.GetByBarcode(ctx, barcode); e == nil {
			barcode = ""
		}
	}
	if barcode == "" {
		if barcode, err = a.p.core.Products.GenerateBarcode(ctx); err != nil {
			return err
		}
	}
	in := products.CreateInput{
		Name:       d.Name,
		CategoryID: catID,
		UnitID:     unitID,
		Barcode:    strPtr(barcode),
		Cost:       c.FormValue("cost"),
		Selling:    c.FormValue("selling"),
	}
	// NOTE: match CreateInput's real field names/types (see internal/features/
	// products/products.go:59). Cost/Selling are strings there ("cost_price"/
	// "selling_price"); Barcode may be *string. Adjust this literal to compile.
	p, err := a.p.core.Products.Create(ctx, in)
	if err != nil {
		return err
	}
	// Set qty on hand via a stock adjustment (mirrors internal/web/intake.go).
	if qty := strings.TrimSpace(c.FormValue("qty")); qty != "" {
		if n, e := decimal.NewFromString(qty); e == nil && n.IsPositive() {
			if e := a.p.core.Stock.Adjust(ctx, stock.AdjustInput{
				ProductID:    p.ID,
				NewQuantity:  n.String(),
				Note:         "AI catalog intake",
				SellingPrice: c.FormValue("selling"),
			}, middleware.CurrentUserID(c)); e != nil {
				return e
			}
		}
	}
	// Learn once.
	_ = a.p.store.LearnItem(ctx, LearnedItem{
		ProductID: p.ID, ResolvedName: d.Name, SuggestedCategory: d.Category,
		Specs: d.Specs, UserExplanation: d.Explanation, Source: d.Source, RawQuery: d.Query,
	})
	// Reset to step 1 + tell the page to fire a label print for N labels.
	labels := c.FormValue("labels")
	trigger := response.Toast("Saved: "+p.Name, "success")
	if labels != "" && labels != "0" {
		trigger = response.ToastAnd("Saved: "+p.Name, "success",
			`{"ai-print-labels":{"product_id":`+strconv.FormatInt(p.ID, 10)+`,"qty":`+labels+`}}`)
	}
	return response.RenderFragment(c, StepIdentify(), trigger)
}
```
Add small helpers:
```go
func strPtr(s string) *string { if s == "" { return nil }; return &s }

func (a *adminUI) defaultUnitID(ctx context.Context) (int64, error) {
	var id int64
	err := a.p.core.DB.GetContext(ctx, &id,
		`SELECT id FROM units ORDER BY (abbreviation = 'pcs') DESC, id ASC LIMIT 1`)
	return id, err
}
```
Add imports as the compiler flags them: `strconv`, `karots-pos/internal/features/products`, `karots-pos/internal/features/stock`, `karots-pos/internal/middleware`, `github.com/shopspring/decimal`.

> **Verify against real signatures before trusting the literal above:** open `internal/features/products/products.go:59` for `CreateInput` field names/types (this plan assumes `Name string`, `CategoryID int64`, `UnitID int64`, `Barcode *string`, `Cost string`/`cost_price`, `Selling string`/`selling_price`). `stock.AdjustInput` is confirmed: `{ProductID int64, NewQuantity string, Note string, SellingPrice string, BatchID int64}`. Fix the `CreateInput` literal to match exactly.

- [ ] **Step 5: Front-end label print hook**

In `pages.templ` `Page`, add a tiny Alpine/JS listener that, on the `ai-print-labels` HX-Trigger event, POSTs to the existing label endpoint so we reuse core printing (no new label code):
```html
<script>
document.body.addEventListener('ai-print-labels', function (e) {
  var d = e.detail;
  var body = new URLSearchParams({ product_id: d.product_id, qty: d.qty, slots: '0', show_price: '1' });
  fetch('/admin/labels/send', { method: 'POST', headers: {'Content-Type':'application/x-www-form-urlencoded'}, body: body });
});
</script>
```
> Verify the exact label route: `grep -n 'labels' internal/web/web.go` (expected `POST /admin/labels/send` → `admin.LabelsSend`). Use `/admin/labels/print` (browser PDF sheet) instead if the shop has no TSPL label printer configured.

- [ ] **Step 6: Add the remaining templ steps**

`StepBarcode` (input `name="barcode"` placed last + autofocus; Enter posts `action=scan`; a separate **Generate** button posts `action=generate`; carries state; if `warn` set, show it red), `StepPrice` (two-mode: a `selling` input with a markup chip showing ×`{markup}` that posts to derive cost, or a `cost` input directly; shows computed cost back), `StepQty` (number input `qty`), `StepLabels` (preset chips + Save). All carry the full hidden-field set forward.

- [ ] **Step 7: templ generate + build**

```
templ generate ./plugins/aicatalog/
go build -o .../scratchpad/pos-server ./cmd/server ; echo "exit=$?"
```
Expected: `exit=0`.

- [ ] **Step 8: Verify full flow end-to-end (Playwright + DB)**

Restart. Add a product start to finish: `6200 2rs` → pick → category → scan a **fresh** barcode (also test scanning an existing one → warns) → selling `500` ×`1.4` → cost shows `357` → qty `10` → labels `= qty` → Save. Then:
```
docker exec pos_db psql -U pos_user -d pos_db -tAc \
 "SELECT p.name, p.selling_price, p.cost_price, c.name AS cat, st.quantity, ai.source
  FROM products p JOIN categories c ON c.id=p.category_id
  LEFT JOIN stock st ON st.product_id=p.id
  LEFT JOIN aicatalog_items ai ON ai.product_id=p.id
  ORDER BY p.id DESC LIMIT 1;"
```
Expected: the new product with cost 357, qty 10, a real category, and an `aicatalog_items` row.

- [ ] **Step 9: Commit**

```bash
git add plugins/aicatalog/admin.go plugins/aicatalog/pages.templ
git commit -m "feat(aicatalog): barcode/price/qty/labels steps + save + label print"
```

---

### Task 6: Cleanup, core-inert check, memory

**Files:**
- Modify: memory files (see below)
- Remove: `cmd/server/enabled_plugins_local.go` (dev-only, never committed)

- [ ] **Step 1: Full test run**

```
go test ./plugins/aicatalog/... ; echo "exit=$?"
```
Expected: PASS.

- [ ] **Step 2: Core-only build (plugin rule)**

```
rm -f cmd/server/enabled_plugins_local.go
go build ./cmd/server ; echo "core exit=$?"
```
Expected: `core exit=0` and the app has no AI Catalog nav (inert without the plugin). Recreate the local enable file afterward only if you keep testing.

- [ ] **Step 3: Revert dev DB test data**

Delete the products created during testing and their `aicatalog_items`/`stock`/`stock_batches`/`stock_movements` rows in one transaction; verify the stock mirror equals the lot sums (per the dev-DB-is-disposable practice). Confirm `aicatalog_settings` keeps only the single row (id=1) — leaving your real key there is fine on the dev box, but scrub it if the DB will be shared:
```
docker exec pos_db psql -U pos_user -d pos_db -c "UPDATE aicatalog_settings SET api_key='' WHERE id=1;"
```

- [ ] **Step 4: Update memory**

- Create `memory/ai-catalog-plugin.md` (type `project`): what it is, the stepper flow, the OpenAI-compatible client + settings, the learn-once table, reuse of `CreateForIntake`-style intake / `stock.Adjust` / `FindOrCreateByPath` / `/admin/labels/send`, and that Phase 2 (Optimize) is deferred and needs categories Merge/Move. Link `[[clearance-plugin]]`, `[[stock-intake-page]]`, `[[batch-dual-pricing-design]]`.
- Add one line to `MEMORY.md` pointing to it.

- [ ] **Step 5: Commit**

```bash
git add memory/ai-catalog-plugin.md memory/MEMORY.md
git commit -m "docs(memory): AI Catalog Assistant plugin (Phase 1)"
```

---

## Self-Review

**Spec coverage:**
- Plugin (not app) → Tasks 3–5. ✓
- Provider-agnostic OpenAI-compatible client + settings + Test key → Task 2, Task 3. ✓
- Conversational stepper, one input, tap-to-pick options, "Other/explain" escape → Tasks 4–5. ✓
- Identify (confident or up-to-4 options) → Task 4. ✓
- Category suggestion via `FindOrCreateByPath` → Tasks 4–5. ✓
- Barcode scan/generate + dup-check → Task 5. ✓
- Cost from markup fraction → Task 1 (helper) + Task 5 (wired). ✓
- Qty on hand → Task 5 (`stock.Adjust`). ✓
- Label print (all/few/custom/none) → Task 5 (reuse `/admin/labels/send`). ✓
- Learn-once persistence, not re-asked → Task 1 (`aicatalog_items`, `LearnItem/GetItem`) + Task 5 (write) + Task 3 (detail row). Phase-2 Optimize reads it. ✓
- Guardrails (key server-side, no silent writes, AI-down fallback, markup guard) → Tasks 2–5. ✓
- Phase 2 (Optimize, vision, grounding) explicitly deferred → not in this plan. ✓

**Placeholder scan:** The only "verify against real code" notes are on `products.CreateInput`'s exact field names and the label route — both are honest signature-confirmation steps with the expected shape given, not deferred work. `ProductDetailContributor.Rows` signature is flagged to match clearance. No TBDs.

**Type consistency:** `IdentifyResult`/`Candidate`/`stepState`/`Settings`/`LearnedItem` defined in Task 1/2 and used consistently in Tasks 3–5. `costFromMarkup(selling, markup) (decimal.Decimal, error)` and `parseIdentify([]byte) (IdentifyResult, error)` signatures match across tasks. `stock.AdjustInput` fields confirmed from source.
