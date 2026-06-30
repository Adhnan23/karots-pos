# UI Revamp — Phase 0b: Component Library + Embedded SVG Icons — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a shared, themeable Templ component library (`templates/ui`) plus an embedded inline-SVG icon set (`templates/ui/icons`) that consume the Phase 0a design tokens, so every page (admin, cashier, plugins) can be recomposed from one consistent, touch- and keyboard-friendly system.

**Architecture:** A new flat package `templates/ui` (package `ui`) mirroring the existing `templates/shared` precedent (plugins already import `templates/shared` and `templates/layouts`, so `templates/ui` is import-cycle-safe — features never import templates). All class strings come from the Phase 0a token utilities (`bg-surface`, `bg-surface-2`, `text-body`, `text-muted`, `border-line`, `bg-accent`, `text-accent-fg`, `bg-accent-weak`, `text-ok/warn/bad/info`, `bg-area-*`/`text-area-*`, `rounded-token`, `p-token`/`gap-token`, `h-control`/`min-h-control`). Class-selection **logic** (variant/size → class) lives in plain Go funcs that are unit-tested; the Templ components are thin wrappers verified by render-to-string smoke tests. Icons are a registry of inline SVG markup rendered with `currentColor` so they inherit each area's accent automatically — shipped inside the Go binary, zero runtime fetch.

**Tech Stack:** Go, Templ (`make templ` generates gitignored `*_templ.go`), Tailwind v3 (`make css` after new utility classes), Alpine.js (for Modal/Sheet/Tabs interactivity), HTMX (component slots stay behavior-agnostic). Tests: Go `testing`, render components via `component.Render(ctx, &buf)`.

## Global Constraints

- Module path: `karots-pos`. New package import path: `karots-pos/templates/ui` (package name `ui`); icons sub-package `karots-pos/templates/ui/icons` (package name `icons`).
- **Presentation only.** No business logic, no new data model, no route behavior changes. Components are markup + class logic + slots; all behavior (hx-*, Alpine) is passed in by the caller via `templ.Attributes`.
- **Fresh design — take NO reference from the existing implementation at all.** Do not read, copy, or imitate the markup, class strings, layout, or components of any existing page OR any existing shared component (`templates/shared/*`, existing layouts, plugin pages). The existing UI is the thing being replaced; it is not a reference of any kind. Design every component from a blank slate against the design tokens, informed only by *what the POS feature needs to do* (the implementer knows the product's full capability set — selling, inventory, purchasing, money/cashflow, reports, setup, plus the recharge & documents plugins — and designs the component to serve that need well). `templates/ui` simply lives beside `templates/shared` for Go import-cycle structure; that is not a style precedent and `templates/shared` must not be imported or consulted. The new library does NOT depend on any existing component.
- **Token-first:** components MUST use the Phase 0a token utility classes, never hardcoded palette colors (no `bg-indigo-600`, no `text-slate-800`). This is what makes them re-themeable.
- **Touch + keyboard:** interactive controls use `min-h-control` (≥44px), visible focus rings are already global (`*:focus-visible` → `var(--ring)` from Phase 0a), Esc closes overlays, everything tab-reachable.
- **Icons:** `fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"`, `viewBox="0 0 24 24"`, original/simple line geometry (no third-party copyrighted path data required). `currentColor` only — never a hardcoded color.
- **Never commit:** `cmd/server/enabled_plugins.go`, `static/css/tailwind.css` (run `make css`, leave unstaged), `.claude/settings.local.json`, any `*_templ.go` (generated + gitignored).
- Commit messages end with: `Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>`.
- Do NOT push; the user pushes. Work on branch `ui-revamp`.
- Render-test helper (used by every task's smoke tests), placed once in Task 1 at `templates/ui/render_test.go`:
  ```go
  package ui

  import (
      "context"
      "strings"
      "testing"

      "github.com/a-h/templ"
  )

  // renderTo renders a templ component to a string for assertion in tests.
  func renderTo(t *testing.T, c templ.Component) string {
      t.Helper()
      var b strings.Builder
      if err := c.Render(context.Background(), &b); err != nil {
          t.Fatalf("render: %v", err)
      }
      return b.String()
  }
  ```

---

### Task 1: UI package scaffold + class helpers + Button

**Files:**
- Create: `templates/ui/class.go` (pure-Go class-selection helpers)
- Create: `templates/ui/class_test.go` (unit tests for the helpers)
- Create: `templates/ui/button.templ` (Button component)
- Create: `templates/ui/render_test.go` (the shared render-test helper from Global Constraints)
- Create: `templates/ui/button_test.go` (render smoke test)

**Interfaces:**
- Produces:
  - `func ButtonClass(variant, size string) string` — variant ∈ {primary, secondary, ghost, danger}, size ∈ {sm, md, lg}; unknown values fall back to primary/md.
  - `type ButtonProps struct { Variant string; Size string; Type string; Attrs templ.Attributes }`
  - `templ Button(p ButtonProps)` — renders `<button>` with class from `ButtonClass`, `type` attr (default "button"), spreads `p.Attrs`, label via `{ children... }`.
  - `renderTo(t, c)` helper (consumed by all later tasks' tests).

- [ ] **Step 1: Write the failing class-helper test**

`templates/ui/class_test.go`:
```go
package ui

import "testing"

func TestButtonClass(t *testing.T) {
    cases := []struct{ variant, size string; wantHas []string }{
        {"primary", "md", []string{"bg-accent", "text-accent-fg", "min-h-control"}},
        {"secondary", "md", []string{"bg-surface-2", "text-body", "border-line"}},
        {"ghost", "sm", []string{"text-body", "text-sm"}},
        {"danger", "lg", []string{"bg-bad", "text-white", "text-base"}},
        {"nonsense", "nonsense", []string{"bg-accent", "text-accent-fg"}}, // fallback primary/md
    }
    for _, c := range cases {
        got := ButtonClass(c.variant, c.size)
        for _, want := range c.wantHas {
            if !contains(got, want) {
                t.Errorf("ButtonClass(%q,%q)=%q, missing %q", c.variant, c.size, got, want)
            }
        }
    }
}

func contains(s, sub string) bool {
    return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}
func indexOf(s, sub string) int {
    for i := 0; i+len(sub) <= len(s); i++ {
        if s[i:i+len(sub)] == sub {
            return i
        }
    }
    return -1
}
```

- [ ] **Step 2: Run it to confirm it fails**

Run: `go test ./templates/ui/ -run TestButtonClass`
Expected: FAIL — `undefined: ButtonClass`.

- [ ] **Step 3: Implement the class helpers**

`templates/ui/class.go`:
```go
// Package ui is the shared, token-driven Templ component library used across
// admin, cashier, and plugin pages. Class-selection logic lives here in plain
// Go so it can be unit-tested; the .templ files are thin wrappers over it.
package ui

// base classes shared by every button variant.
const btnBase = "inline-flex items-center justify-center gap-2 rounded-token font-medium min-h-control transition-colors focus-visible:outline-none disabled:opacity-50 disabled:pointer-events-none"

// ButtonClass returns the full class string for a button variant+size.
// Unknown variant falls back to primary; unknown size to md.
func ButtonClass(variant, size string) string {
    v := map[string]string{
        "primary":   "bg-accent text-accent-fg hover:opacity-90",
        "secondary": "bg-surface-2 text-body border border-line hover:bg-surface",
        "ghost":     "bg-transparent text-body hover:bg-surface-2",
        "danger":    "bg-bad text-white hover:opacity-90",
    }[variant]
    if v == "" {
        v = "bg-accent text-accent-fg hover:opacity-90"
    }
    s := map[string]string{
        "sm": "text-sm px-3 py-1.5",
        "md": "text-sm px-4 py-2",
        "lg": "text-base px-5 py-2.5",
    }[size]
    if s == "" {
        s = "text-sm px-4 py-2"
    }
    return btnBase + " " + v + " " + s
}
```

- [ ] **Step 4: Run the class test to confirm it passes**

Run: `go test ./templates/ui/ -run TestButtonClass`
Expected: PASS.

- [ ] **Step 5: Add the render-test helper and the Button component**

Create `templates/ui/render_test.go` with the exact helper from Global Constraints.

Create `templates/ui/button.templ`:
```templ
package ui

// ButtonProps configures the Button component. Attrs spreads arbitrary
// attributes (hx-*, x-on:click, name, value, disabled, ...) onto the element so
// the component stays behavior-agnostic.
type ButtonProps struct {
	Variant string // primary | secondary | ghost | danger (default primary)
	Size    string // sm | md | lg (default md)
	Type    string // button | submit (default button)
	Attrs   templ.Attributes
}

templ Button(p ButtonProps) {
	<button
		type={ buttonType(p.Type) }
		class={ ButtonClass(p.Variant, p.Size) }
		{ p.Attrs... }
	>
		{ children... }
	</button>
}
```

Add `buttonType` to `templates/ui/class.go`:
```go
// buttonType defaults the button type attribute to "button" so a component
// dropped inside a form never submits by accident.
func buttonType(t string) string {
    if t == "submit" {
        return "submit"
    }
    return "button"
}
```

- [ ] **Step 6: Write the Button render smoke test**

`templates/ui/button_test.go`:
```go
package ui

import (
    "testing"

    "github.com/a-h/templ"
)

func TestButtonRenders(t *testing.T) {
    html := renderTo(t, Button(ButtonProps{
        Variant: "primary",
        Attrs:   templ.Attributes{"hx-get": "/x", "disabled": true},
    }))
    for _, want := range []string{"<button", `type="button"`, "bg-accent", `hx-get="/x"`, "disabled"} {
        if !contains(html, want) {
            t.Errorf("Button render missing %q in:\n%s", want, html)
        }
    }
}

func TestButtonSubmitType(t *testing.T) {
    html := renderTo(t, Button(ButtonProps{Type: "submit"}))
    if !contains(html, `type="submit"`) {
        t.Errorf("expected submit type, got:\n%s", html)
    }
}
```

- [ ] **Step 7: Generate templ, run tests, vet, build**

Run:
```bash
make templ && go test ./templates/ui/ -v && go vet ./templates/ui/ && go build ./...
```
Expected: all PASS, vet clean, build OK.

- [ ] **Step 8: Commit**

```bash
git add templates/ui/class.go templates/ui/class_test.go templates/ui/button.templ \
  templates/ui/render_test.go templates/ui/button_test.go
git commit -m "feat(ui): scaffold token-driven component package + Button

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

### Task 2: Embedded inline-SVG icon set

**Files:**
- Create: `templates/ui/icons/icons.go` (registry + render logic)
- Create: `templates/ui/icons/icons.templ` (the `Icon` component)
- Create: `templates/ui/icons/icons_test.go` (registry + render tests)

**Interfaces:**
- Consumes: nothing from earlier tasks (standalone sub-package).
- Produces:
  - `func Has(name string) bool` — reports whether an icon name is registered.
  - `func Names() []string` — sorted list of all registered icon names (used by the gallery in Task 7).
  - `templ Icon(name, class string)` — renders `<svg viewBox="0 0 24 24" ... class={class}>` with the named geometry; unknown name renders an empty (but valid) `<svg>` so layout never breaks.

**Rationale for an in-task authored set:** path geometry must be visually verified, so the implementer authors each icon's inner SVG and eyeballs it (Task 7 gallery), rather than transcribing third-party path data blind. The mechanism, API, and tests below are fixed; the per-icon geometry is the implementer's deliverable, following the worked examples.

- [ ] **Step 1: Write the failing registry test**

`templates/ui/icons/icons_test.go`:
```go
package icons

import (
    "context"
    "strings"
    "testing"
)

// requiredIcons MUST all be registered — these are consumed by nav, areas,
// and common actions in later phases.
var requiredIcons = []string{
    // areas / nav
    "home", "box", "boxes", "receipt", "cart", "wallet", "chart", "settings",
    "users", "truck",
    // actions
    "plus", "search", "edit", "trash", "check", "x", "chevron-right",
    "chevron-down", "menu", "filter", "download", "print", "refresh", "logout",
    "palette",
}

func TestRequiredIconsRegistered(t *testing.T) {
    for _, n := range requiredIcons {
        if !Has(n) {
            t.Errorf("required icon %q not registered", n)
        }
    }
}

func TestIconRenders(t *testing.T) {
    var b strings.Builder
    if err := Icon("box", "w-5 h-5 text-area-inventory").Render(context.Background(), &b); err != nil {
        t.Fatal(err)
    }
    out := b.String()
    for _, want := range []string{"<svg", `viewBox="0 0 24 24"`, `stroke="currentColor"`, "w-5 h-5 text-area-inventory"} {
        if !strings.Contains(out, want) {
            t.Errorf("Icon render missing %q in:\n%s", want, out)
        }
    }
}

func TestUnknownIconIsSafe(t *testing.T) {
    var b strings.Builder
    if err := Icon("__nope__", "w-4 h-4").Render(context.Background(), &b); err != nil {
        t.Fatal(err)
    }
    out := b.String()
    if !strings.Contains(out, "<svg") || !strings.Contains(out, "</svg>") {
        t.Errorf("unknown icon should still render a valid empty svg, got:\n%s", out)
    }
}
```

- [ ] **Step 2: Run it to confirm it fails**

Run: `go test ./templates/ui/icons/`
Expected: FAIL — `undefined: Has` / `undefined: Icon`.

- [ ] **Step 3: Implement the registry**

`templates/ui/icons/icons.go`:
```go
// Package icons holds the embedded inline-SVG icon set. Each icon is the INNER
// markup of a 24x24 stroke icon; the Icon component wraps it in an <svg> with
// currentColor so icons inherit the surrounding text color (and thus each
// area's accent). Shipped inside the binary — no runtime fetch.
package icons

import "sort"

// paths maps an icon name to its inner SVG markup (paths/shapes only).
// Geometry is original simple line art per the project icon rules.
var paths = map[string]string{
    // --- worked examples (the pattern; implementer authors the rest) ---
    "plus":          `<line x1="12" y1="5" x2="12" y2="19"/><line x1="5" y1="12" x2="19" y2="12"/>`,
    "x":             `<line x1="18" y1="6" x2="6" y2="18"/><line x1="6" y1="6" x2="18" y2="18"/>`,
    "check":         `<polyline points="20 6 9 17 4 12"/>`,
    "search":        `<circle cx="11" cy="11" r="7"/><line x1="21" y1="21" x2="16.65" y2="16.65"/>`,
    "box":           `<path d="M21 8v8a1 1 0 0 1-.5.87l-7.5 4.3a2 2 0 0 1-2 0l-7.5-4.3A1 1 0 0 1 3 16V8"/><path d="m3.3 7 8.2 4.7a1 1 0 0 0 1 0L20.7 7"/><path d="m12 3 8.5 4.9a1 1 0 0 1 0 .2v0L12 13 3.5 8.1v0a1 1 0 0 1 0-.2Z"/>`,
    "chevron-right": `<polyline points="9 18 15 12 9 6"/>`,
    "chevron-down":  `<polyline points="6 9 12 15 18 9"/>`,
    // The implementer adds the remaining requiredIcons (see icons_test.go):
    // home, boxes, receipt, cart, wallet, chart, settings, users, truck,
    // edit, trash, menu, filter, download, print, refresh, logout, palette.
    // Each value is inner SVG using <path>/<line>/<circle>/<rect>/<polyline>
    // with no fill/stroke attrs (the wrapper sets stroke=currentColor).
}

// Has reports whether an icon name is registered.
func Has(name string) bool { _, ok := paths[name]; return ok }

// Names returns all registered icon names, sorted.
func Names() []string {
    out := make([]string, 0, len(paths))
    for k := range paths {
        out = append(out, k)
    }
    sort.Strings(out)
    return out
}

// inner returns the raw inner SVG for a name, or "" if unknown.
func inner(name string) string { return paths[name] }
```

- [ ] **Step 4: Implement the Icon component**

`templates/ui/icons/icons.templ`:
```templ
package icons

// Icon renders a registered 24x24 stroke icon. Color comes from the
// surrounding text color via currentColor; size/color via the class arg
// (e.g. "w-5 h-5 text-area-sell"). Unknown names render a valid empty svg.
templ Icon(name, class string) {
	<svg
		xmlns="http://www.w3.org/2000/svg"
		viewBox="0 0 24 24"
		fill="none"
		stroke="currentColor"
		stroke-width="2"
		stroke-linecap="round"
		stroke-linejoin="round"
		class={ class }
		aria-hidden="true"
	>
		@templ.Raw(inner(name))
	</svg>
}
```

- [ ] **Step 5: Author the remaining required icons**

In `templates/ui/icons/icons.go`, add inner SVG for every name in `requiredIcons` not already present (home, boxes, receipt, cart, wallet, chart, settings, users, truck, edit, trash, menu, filter, download, print, refresh, logout, palette). Follow the worked-example pattern (simple `<path>`/`<line>`/`<circle>`/`<rect>`/`<polyline>`, no per-shape color). Keep each recognizable at 20–24px.

- [ ] **Step 6: Generate templ, run tests, vet, build**

Run:
```bash
make templ && go test ./templates/ui/icons/ -v && go vet ./templates/ui/icons/ && go build ./...
```
Expected: all PASS (incl. `TestRequiredIconsRegistered`), vet clean, build OK.

- [ ] **Step 7: Commit**

```bash
git add templates/ui/icons/icons.go templates/ui/icons/icons.templ templates/ui/icons/icons_test.go
git commit -m "feat(ui): embedded inline-SVG icon set (currentColor, in-binary)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

### Task 3: Form controls (Field wrapper + Input, Select, Textarea, Checkbox, Toggle)

**Files:**
- Create: `templates/ui/form.templ` (components)
- Create: `templates/ui/form.go` (control class constants + option type)
- Create: `templates/ui/form_test.go` (render smoke tests)

**Interfaces:**
- Consumes: `renderTo` (Task 1).
- Produces:
  - `const ControlClass` — shared input/select/textarea class string (token-driven, `min-h-control`).
  - `type Option struct { Value, Label string }`
  - `type FieldProps struct { Label, Hint, Error string; Required bool }`
  - `templ Field(p FieldProps)` — label + `{ children... }` (the control) + optional hint/error text. Wrapper only.
  - `templ Input(attrs templ.Attributes)` — `<input class={ControlClass} {attrs...}/>`
  - `templ Textarea(attrs templ.Attributes)` — `<textarea class={ControlClass} {attrs...}></textarea>`
  - `templ Select(options []Option, selected string, attrs templ.Attributes)` — `<select>` with options, marking `selected`.
  - `templ Checkbox(label string, attrs templ.Attributes)` — checkbox + inline label, ≥44px row.
  - `templ Toggle(label string, attrs templ.Attributes)` — switch-style checkbox.

- [ ] **Step 1: Write the failing render test**

`templates/ui/form_test.go`:
```go
package ui

import (
    "testing"

    "github.com/a-h/templ"
)

func TestInputRenders(t *testing.T) {
    html := renderTo(t, Input(templ.Attributes{"name": "qty", "inputmode": "numeric"}))
    for _, want := range []string{"<input", `name="qty"`, `inputmode="numeric"`, "min-h-control"} {
        if !contains(html, want) {
            t.Errorf("Input missing %q in:\n%s", want, html)
        }
    }
}

func TestSelectMarksSelected(t *testing.T) {
    opts := []Option{{"a", "Apple"}, {"b", "Banana"}}
    html := renderTo(t, Select(opts, "b", templ.Attributes{"name": "fruit"}))
    if !contains(html, `value="b" selected`) && !contains(html, `selected`) {
        t.Errorf("Select should mark selected option, got:\n%s", html)
    }
    if !contains(html, "Banana") || !contains(html, `name="fruit"`) {
        t.Errorf("Select missing options/name in:\n%s", html)
    }
}

func TestFieldShowsError(t *testing.T) {
    html := renderTo(t, Field(FieldProps{Label: "Name", Error: "required", Required: true}))
    for _, want := range []string{"Name", "required", "text-bad"} {
        if !contains(html, want) {
            t.Errorf("Field missing %q in:\n%s", want, html)
        }
    }
}
```

- [ ] **Step 2: Run it to confirm it fails**

Run: `go test ./templates/ui/ -run 'TestInput|TestSelect|TestField'`
Expected: FAIL — undefined `Input`/`Select`/`Field`/`Option`/`FieldProps`.

- [ ] **Step 3: Implement the shared class + Option type**

`templates/ui/form.go`:
```go
package ui

// ControlClass is the shared class for text inputs, selects, and textareas.
// Token-driven so it recolors with the active theme; min-h-control keeps a
// 44px+ touch target.
const ControlClass = "w-full rounded-token border border-line bg-surface text-body placeholder:text-muted px-3 py-2 min-h-control focus-visible:outline-none"

// Option is a single <select> option.
type Option struct{ Value, Label string }

// FieldProps configures the Field label/hint/error wrapper.
type FieldProps struct {
    Label    string
    Hint     string
    Error    string
    Required bool
}
```

- [ ] **Step 4: Implement the components**

`templates/ui/form.templ`:
```templ
package ui

templ Field(p FieldProps) {
	<label class="block space-y-1">
		<span class="text-sm font-medium text-body">
			{ p.Label }
			if p.Required {
				<span class="text-bad">*</span>
			}
		</span>
		{ children... }
		if p.Hint != "" && p.Error == "" {
			<span class="block text-xs text-muted">{ p.Hint }</span>
		}
		if p.Error != "" {
			<span class="block text-xs text-bad">{ p.Error }</span>
		}
	</label>
}

templ Input(attrs templ.Attributes) {
	<input class={ ControlClass } { attrs... }/>
}

templ Textarea(attrs templ.Attributes) {
	<textarea class={ ControlClass } { attrs... }></textarea>
}

templ Select(options []Option, selected string, attrs templ.Attributes) {
	<select class={ ControlClass } { attrs... }>
		for _, o := range options {
			if o.Value == selected {
				<option value={ o.Value } selected>{ o.Label }</option>
			} else {
				<option value={ o.Value }>{ o.Label }</option>
			}
		}
	</select>
}

templ Checkbox(label string, attrs templ.Attributes) {
	<label class="flex items-center gap-2 min-h-control cursor-pointer">
		<input type="checkbox" class="w-5 h-5 rounded border-line text-accent" { attrs... }/>
		<span class="text-sm text-body">{ label }</span>
	</label>
}

templ Toggle(label string, attrs templ.Attributes) {
	<label class="flex items-center gap-2 min-h-control cursor-pointer">
		<input type="checkbox" class="w-5 h-5 rounded border-line text-accent" { attrs... }/>
		<span class="text-sm text-body">{ label }</span>
	</label>
}
```

- [ ] **Step 5: Generate templ, run tests, vet, build**

Run:
```bash
make templ && go test ./templates/ui/ -run 'TestInput|TestSelect|TestField' -v && go vet ./templates/ui/ && go build ./...
```
Expected: PASS, vet clean, build OK.

- [ ] **Step 6: Commit**

```bash
git add templates/ui/form.go templates/ui/form.templ templates/ui/form_test.go
git commit -m "feat(ui): form controls — Field wrapper, Input, Select, Textarea, Checkbox, Toggle

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

### Task 4: Containers — Card, SectionHeader, PageHeader, EmptyState

**Files:**
- Create: `templates/ui/layout.templ` (components)
- Create: `templates/ui/layout.go` (Breadcrumb/PageHeaderProps types)
- Create: `templates/ui/layout_test.go` (render smoke tests)

**Interfaces:**
- Consumes: `renderTo` (Task 1), `icons.Icon` (Task 2).
- Produces:
  - `type Crumb struct { Label, Href string }`
  - `type PageHeaderProps struct { Title, Subtitle string; Crumbs []Crumb }`
  - `templ Card()` — `{ children... }` inside a `bg-surface border-line rounded-token` panel.
  - `templ SectionHeader(title string)` — `{ children... }` is the right-aligned actions slot.
  - `templ PageHeader(p PageHeaderProps)` — breadcrumb + title + subtitle, `{ children... }` is the actions slot (e.g. a primary Button).
  - `templ EmptyState(icon, title, message string)` — centered icon + title + message, `{ children... }` is an optional action slot.

- [ ] **Step 1: Write the failing render test**

`templates/ui/layout_test.go`:
```go
package ui

import "testing"

func TestPageHeaderRenders(t *testing.T) {
    html := renderTo(t, PageHeader(PageHeaderProps{
        Title:    "Products",
        Subtitle: "Manage your catalog",
        Crumbs:   []Crumb{{"Home", "/admin"}, {"Products", ""}},
    }))
    for _, want := range []string{"Products", "Manage your catalog", "Home", `href="/admin"`} {
        if !contains(html, want) {
            t.Errorf("PageHeader missing %q in:\n%s", want, html)
        }
    }
}

func TestEmptyStateRenders(t *testing.T) {
    html := renderTo(t, EmptyState("box", "No products", "Add your first product to begin"))
    for _, want := range []string{"<svg", "No products", "Add your first product"} {
        if !contains(html, want) {
            t.Errorf("EmptyState missing %q in:\n%s", want, html)
        }
    }
}
```

- [ ] **Step 2: Run it to confirm it fails**

Run: `go test ./templates/ui/ -run 'TestPageHeader|TestEmptyState'`
Expected: FAIL — undefined `PageHeader`/`EmptyState`/types.

- [ ] **Step 3: Implement the types**

`templates/ui/layout.go`:
```go
package ui

// Crumb is one breadcrumb entry. An empty Href renders as the current
// (non-link) page.
type Crumb struct{ Label, Href string }

// PageHeaderProps configures the page header (breadcrumb + title + actions).
type PageHeaderProps struct {
    Title    string
    Subtitle string
    Crumbs   []Crumb
}
```

- [ ] **Step 4: Implement the components**

`templates/ui/layout.templ`:
```templ
package ui

import "karots-pos/templates/ui/icons"

templ Card() {
	<div class="bg-surface border border-line rounded-token p-token">
		{ children... }
	</div>
}

templ SectionHeader(title string) {
	<div class="flex items-center justify-between gap-3 mb-3">
		<h2 class="text-lg font-semibold text-body">{ title }</h2>
		<div class="flex items-center gap-2">
			{ children... }
		</div>
	</div>
}

templ PageHeader(p PageHeaderProps) {
	<div class="mb-6 space-y-2">
		if len(p.Crumbs) > 0 {
			<nav class="flex items-center gap-1 text-xs text-muted">
				for i, c := range p.Crumbs {
					if i > 0 {
						@icons.Icon("chevron-right", "w-3 h-3")
					}
					if c.Href != "" {
						<a href={ templ.SafeURL(c.Href) } class="hover:text-body">{ c.Label }</a>
					} else {
						<span class="text-body">{ c.Label }</span>
					}
				}
			</nav>
		}
		<div class="flex flex-wrap items-center justify-between gap-3">
			<div>
				<h1 class="text-2xl font-bold text-body">{ p.Title }</h1>
				if p.Subtitle != "" {
					<p class="text-sm text-muted">{ p.Subtitle }</p>
				}
			</div>
			<div class="flex items-center gap-2">
				{ children... }
			</div>
		</div>
	</div>
}

templ EmptyState(icon, title, message string) {
	<div class="flex flex-col items-center justify-center text-center py-12 px-4 gap-3">
		<div class="text-muted">
			@icons.Icon(icon, "w-10 h-10")
		</div>
		<div class="text-lg font-semibold text-body">{ title }</div>
		<p class="text-sm text-muted max-w-sm">{ message }</p>
		<div class="mt-2">
			{ children... }
		</div>
	</div>
}
```

- [ ] **Step 5: Generate templ, run tests, vet, build**

Run:
```bash
make templ && go test ./templates/ui/ -run 'TestPageHeader|TestEmptyState' -v && go vet ./templates/ui/ && go build ./...
```
Expected: PASS, vet clean, build OK.

- [ ] **Step 6: Commit**

```bash
git add templates/ui/layout.go templates/ui/layout.templ templates/ui/layout_test.go
git commit -m "feat(ui): containers — Card, SectionHeader, PageHeader, EmptyState

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

### Task 5: Display — StatTile, ActionTile, Badge, responsive Table

**Files:**
- Create: `templates/ui/display.templ` (components)
- Create: `templates/ui/display.go` (Badge tone class + props types)
- Create: `templates/ui/display_test.go` (unit + render tests)

**Interfaces:**
- Consumes: `renderTo` (Task 1), `icons.Icon` (Task 2).
- Produces:
  - `func BadgeClass(tone string) string` — tone ∈ {neutral, ok, warn, bad, info, accent}; unknown → neutral.
  - `templ Badge(tone string)` — pill, `{ children... }` label.
  - `type StatTileProps struct { Label, Value, Sub, Icon, Area string }` — Area ∈ area accent keys (sell/inventory/...) or "".
  - `templ StatTile(p StatTileProps)` — value-forward metric card.
  - `type ActionTileProps struct { Title, Desc, Href, Icon, Area string }`
  - `templ ActionTile(p ActionTileProps)` — big icon nav tile (links to Href), colored by Area.
  - `templ Table(headers []string)` — `<table>` that collapses to stacked rows on phone (`{ children... }` are the `<tr>`s the caller supplies).

- [ ] **Step 1: Write the failing tests**

`templates/ui/display_test.go`:
```go
package ui

import "testing"

func TestBadgeClass(t *testing.T) {
    cases := map[string]string{
        "ok":      "bg-ok",
        "bad":     "bg-bad",
        "accent":  "bg-accent",
        "nonsense": "bg-surface-2", // neutral fallback
    }
    for tone, want := range cases {
        if got := BadgeClass(tone); !contains(got, want) {
            t.Errorf("BadgeClass(%q)=%q missing %q", tone, got, want)
        }
    }
}

func TestActionTileRenders(t *testing.T) {
    html := renderTo(t, ActionTile(ActionTileProps{
        Title: "New Sale", Desc: "Start a transaction", Href: "/cashier", Icon: "receipt", Area: "sell",
    }))
    for _, want := range []string{"New Sale", "Start a transaction", `href="/cashier"`, "<svg", "area-sell"} {
        if !contains(html, want) {
            t.Errorf("ActionTile missing %q in:\n%s", want, html)
        }
    }
}

func TestStatTileRenders(t *testing.T) {
    html := renderTo(t, StatTile(StatTileProps{Label: "Today's sales", Value: "Rs 12,500", Icon: "chart", Area: "reports"}))
    for _, want := range []string{"Today's sales", "Rs 12,500", "<svg"} {
        if !contains(html, want) {
            t.Errorf("StatTile missing %q in:\n%s", want, html)
        }
    }
}
```

- [ ] **Step 2: Run it to confirm it fails**

Run: `go test ./templates/ui/ -run 'TestBadge|TestActionTile|TestStatTile'`
Expected: FAIL — undefined symbols.

- [ ] **Step 3: Implement classes/props**

`templates/ui/display.go`:
```go
package ui

// BadgeClass returns the pill class for a tone. Unknown tone → neutral.
func BadgeClass(tone string) string {
    base := "inline-flex items-center gap-1 px-2 py-0.5 rounded-full text-xs font-medium "
    switch tone {
    case "ok":
        return base + "bg-ok text-white"
    case "warn":
        return base + "bg-warn text-white"
    case "bad":
        return base + "bg-bad text-white"
    case "info":
        return base + "bg-info text-white"
    case "accent":
        return base + "bg-accent text-accent-fg"
    default:
        return base + "bg-surface-2 text-body"
    }
}

// areaText returns the area-accent text class, or "text-accent" when no area.
func areaText(area string) string {
    if area == "" {
        return "text-accent"
    }
    return "text-area-" + area
}

// StatTileProps configures a metric tile.
type StatTileProps struct {
    Label string
    Value string
    Sub   string
    Icon  string
    Area  string
}

// ActionTileProps configures a big icon nav tile.
type ActionTileProps struct {
    Title string
    Desc  string
    Href  string
    Icon  string
    Area  string
}
```

- [ ] **Step 4: Implement the components**

`templates/ui/display.templ`:
```templ
package ui

import "karots-pos/templates/ui/icons"

templ Badge(tone string) {
	<span class={ BadgeClass(tone) }>
		{ children... }
	</span>
}

templ StatTile(p StatTileProps) {
	<div class="bg-surface border border-line rounded-token p-4 flex items-start justify-between gap-3">
		<div>
			<div class="text-sm text-muted">{ p.Label }</div>
			<div class="text-2xl font-bold text-body tabular-nums">{ p.Value }</div>
			if p.Sub != "" {
				<div class="text-xs text-muted">{ p.Sub }</div>
			}
		</div>
		if p.Icon != "" {
			<div class={ areaText(p.Area) }>
				@icons.Icon(p.Icon, "w-7 h-7")
			</div>
		}
	</div>
}

templ ActionTile(p ActionTileProps) {
	<a
		href={ templ.SafeURL(p.Href) }
		class="group bg-surface border border-line rounded-token p-5 flex flex-col gap-3 min-h-control hover:border-accent hover:shadow-sm transition"
	>
		<div class={ areaText(p.Area) }>
			@icons.Icon(p.Icon, "w-8 h-8")
		</div>
		<div>
			<div class="font-semibold text-body">{ p.Title }</div>
			if p.Desc != "" {
				<div class="text-sm text-muted">{ p.Desc }</div>
			}
		</div>
	</a>
}

// Table renders a responsive table. On phone widths the header row is hidden and
// each row is expected to stack (the caller's <td>s use `before:` labels or the
// stacked card pattern). headers drive the desktop <thead>.
templ Table(headers []string) {
	<div class="overflow-x-auto -mx-1">
		<table class="w-full text-sm">
			<thead class="hidden sm:table-header-group">
				<tr class="text-left text-muted border-b border-line">
					for _, h := range headers {
						<th class="py-2 px-3 font-medium">{ h }</th>
					}
				</tr>
			</thead>
			<tbody class="divide-y divide-line">
				{ children... }
			</tbody>
		</table>
	</div>
}
```

- [ ] **Step 5: Generate templ, run tests, vet, build**

Run:
```bash
make templ && go test ./templates/ui/ -run 'TestBadge|TestActionTile|TestStatTile' -v && go vet ./templates/ui/ && go build ./...
```
Expected: PASS, vet clean, build OK.

- [ ] **Step 6: Commit**

```bash
git add templates/ui/display.go templates/ui/display.templ templates/ui/display_test.go
git commit -m "feat(ui): display — StatTile, ActionTile, Badge, responsive Table

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

### Task 6: Overlays & navigation — Modal/Sheet, Tabs, FilterBar, DateRangeBar

**Files:**
- Create: `templates/ui/overlay.templ` (Modal/Sheet)
- Create: `templates/ui/nav.templ` (Tabs, FilterBar, DateRangeBar)
- Create: `templates/ui/nav.go` (Tab type)
- Create: `templates/ui/overlay_test.go` and `templates/ui/nav_test.go` (render tests)

**Interfaces:**
- Consumes: `renderTo` (Task 1), `icons.Icon` (Task 2).
- Produces:
  - `templ Modal(id, title string)` — Alpine-driven dialog; bottom-sheet on phone, centered on desktop; Esc + ✕ + backdrop close. Opened by the caller dispatching `$dispatch('open-modal-'+id)` or toggling via the shared host (see Step 4). `{ children... }` is the body.
  - `type Tab struct { Label, Target, Key string }`
  - `templ Tabs(tabs []Tab, active string)` — token-styled tab strip; each tab is an `<a href=Target>` (server-rendered tabs). Active tab uses the accent underline.
  - `templ FilterBar()` — responsive wrapper row for filter controls (`{ children... }`).
  - `type RangePreset struct { Key, Label string }`
  - `templ DateRangeBar(action, preset, from, to string)` — **authored fresh**. Renders token-styled preset chips + from/to date inputs. It is a self-contained control: it GETs to a generic `action` URL (supplied by whichever page adopts it in a later phase) with `preset`/`from`/`to` query params. The preset chips use clean, conventional keys — `today`, `this-week`, `this-month`, `last-week`, `last-month`, `this-year` — chosen as good date-picker UX, not borrowed from any existing page.

- [ ] **Step 1: Write the failing tests**

`templates/ui/nav_test.go`:
```go
package ui

import "testing"

func TestTabsMarksActive(t *testing.T) {
    tabs := []Tab{{"Sales", "/admin/receipts?tab=sales", "sales"}, {"Cash", "/admin/receipts?tab=cash", "cash"}}
    html := renderTo(t, Tabs(tabs, "cash"))
    for _, want := range []string{"Sales", "Cash", `href="/admin/receipts?tab=sales"`, "border-accent"} {
        if !contains(html, want) {
            t.Errorf("Tabs missing %q in:\n%s", want, html)
        }
    }
}

func TestDateRangeBarDelegates(t *testing.T) {
    html := renderTo(t, DateRangeBar("/admin/reports/sales", "today", "", ""))
    // shared.RangeForm renders preset buttons + from/to inputs
    for _, want := range []string{"Today", "From", "To"} {
        if !contains(html, want) {
            t.Errorf("DateRangeBar missing %q in:\n%s", want, html)
        }
    }
}
```

`templates/ui/overlay_test.go`:
```go
package ui

import "testing"

func TestModalRenders(t *testing.T) {
    html := renderTo(t, Modal("confirm", "Are you sure?"))
    for _, want := range []string{"Are you sure?", "open-modal-confirm", "x-show", "<svg"} {
        if !contains(html, want) {
            t.Errorf("Modal missing %q in:\n%s", want, html)
        }
    }
}
```

- [ ] **Step 2: Run it to confirm it fails**

Run: `go test ./templates/ui/ -run 'TestTabs|TestDateRangeBar|TestModal'`
Expected: FAIL — undefined `Tabs`/`DateRangeBar`/`Modal`/`Tab`.

- [ ] **Step 3: Implement the Tab + RangePreset types**

`templates/ui/nav.go`:
```go
package ui

// Tab is one entry in a Tabs strip. Target is the href; Key matches the active
// argument to mark the current tab.
type Tab struct {
    Label  string
    Target string
    Key    string
}

// RangePreset is one quick date-range chip. Key is the value sent as ?preset=
// to the adopting page's action URL.
type RangePreset struct{ Key, Label string }

// rangePresets is the fixed set of quick ranges, authored fresh for the new
// date bar — conventional date-picker options, not borrowed from any page.
var rangePresets = []RangePreset{
    {"today", "Today"},
    {"this-week", "This week"},
    {"this-month", "This month"},
    {"last-week", "Last week"},
    {"last-month", "Last month"},
    {"this-year", "This year"},
}
```

- [ ] **Step 4: Implement overlays**

`templates/ui/overlay.templ`:
```templ
package ui

import "karots-pos/templates/ui/icons"

// Modal is an Alpine-driven dialog: centered on desktop, bottom-sheet on phone.
// Open it from anywhere with: x-on:click="$dispatch('open-modal-<id>')".
// Closes on Esc, the ✕ button, or backdrop click.
templ Modal(id, title string) {
	<div
		x-data="{ open: false }"
		x-on:open-modal={ "window.addEventListener('open-modal-" + id + "', () => open = true)" }
		x-on:keydown.escape.window="open = false"
		x-show="open"
		x-cloak
		class="fixed inset-0 z-50 flex items-end sm:items-center justify-center"
	>
		<div class="absolute inset-0 bg-black/40" x-on:click="open = false"></div>
		<div class="relative bg-surface text-body w-full sm:max-w-lg sm:rounded-token rounded-t-token shadow-xl p-token max-h-[90vh] overflow-y-auto">
			<div class="flex items-center justify-between gap-3 mb-3">
				<h3 class="text-lg font-semibold">{ title }</h3>
				<button type="button" x-on:click="open = false" class="text-muted hover:text-body" aria-label="Close">
					@icons.Icon("x", "w-5 h-5")
				</button>
			</div>
			{ children... }
		</div>
	</div>
}
```

Note: `x-on:open-modal` is a harmless no-op attribute carrier; the actual listener is the `window.addEventListener` expression, which Alpine evaluates on init. (Implementer: if the linter prefers, move the listener into an `x-init` attribute instead — functionally identical. Verify the dispatch/open cycle in the Task 7 gallery.)

- [ ] **Step 5: Implement nav components**

`templates/ui/nav.templ`:
```templ
package ui

templ Tabs(tabs []Tab, active string) {
	<div class="border-b border-line mb-4">
		<nav class="flex flex-wrap gap-1 -mb-px">
			for _, tb := range tabs {
				if tb.Key == active {
					<a href={ templ.SafeURL(tb.Target) } class="px-4 py-2 border-b-2 border-accent text-accent font-medium min-h-control inline-flex items-center">{ tb.Label }</a>
				} else {
					<a href={ templ.SafeURL(tb.Target) } class="px-4 py-2 border-b-2 border-transparent text-muted hover:text-body min-h-control inline-flex items-center">{ tb.Label }</a>
				}
			}
		</nav>
	</div>
}

templ FilterBar() {
	<div class="flex flex-wrap items-end gap-3 mb-4">
		{ children... }
	</div>
}

// DateRangeBar is a fresh token-styled date range control: quick preset chips +
// exact from/to inputs. It keeps only the server contract (GET action with
// preset/from/to → reports.ResolveRange); the markup is new, not borrowed.
templ DateRangeBar(action, preset, from, to string) {
	<div class="no-print mb-4 space-y-3">
		<div class="flex flex-wrap gap-2">
			for _, rp := range rangePresets {
				if rp.Key == preset {
					<a href={ templ.SafeURL(action + "?preset=" + rp.Key) } class="px-3 py-1.5 rounded-token bg-accent text-accent-fg text-sm min-h-control inline-flex items-center">{ rp.Label }</a>
				} else {
					<a href={ templ.SafeURL(action + "?preset=" + rp.Key) } class="px-3 py-1.5 rounded-token border border-line text-muted hover:text-body hover:bg-surface-2 text-sm min-h-control inline-flex items-center">{ rp.Label }</a>
				}
			}
		</div>
		<form method="get" action={ templ.SafeURL(action) } class="flex flex-wrap items-end gap-3">
			<label class="text-sm text-body">
				<span class="block text-xs text-muted mb-1">From</span>
				<input type="date" name="from" value={ from } class={ ControlClass }/>
			</label>
			<label class="text-sm text-body">
				<span class="block text-xs text-muted mb-1">To</span>
				<input type="date" name="to" value={ to } class={ ControlClass }/>
			</label>
			@Button(ButtonProps{Variant: "secondary", Type: "submit"}) {
				Apply
			}
		</form>
	</div>
}
```

- [ ] **Step 6: Generate templ, run tests, vet, build**

Run:
```bash
make templ && go test ./templates/ui/ -run 'TestTabs|TestDateRangeBar|TestModal' -v && go vet ./templates/ui/ && go build ./...
```
Expected: PASS, vet clean, build OK.

- [ ] **Step 7: Commit**

```bash
git add templates/ui/nav.go templates/ui/nav.templ templates/ui/overlay.templ \
  templates/ui/nav_test.go templates/ui/overlay_test.go
git commit -m "feat(ui): overlays + nav — Modal/Sheet, Tabs, FilterBar, DateRangeBar

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

### Task 7: UI gallery page (visual verification) + route + final build

**Files:**
- Create: `templates/pages/admin/uigallery.templ` (the gallery page, package `adminpages`)
- Create: `internal/web/admin_uigallery.go` (handler)
- Modify: `internal/web/web.go` (register `GET /admin/ui` in the admin group)

**Interfaces:**
- Consumes: every `ui.*` component (Tasks 1, 3, 4, 5, 6), `icons.Icon` + `icons.Names` (Task 2).
- Produces: route `GET /admin/ui` rendering a full-page gallery of all components and all icons — the human visual-QA surface for the whole library.

- [ ] **Step 1: Write the gallery page**

Create `templates/pages/admin/uigallery.templ`:
```templ
package adminpages

import (
	"karots-pos/templates/layouts"
	"karots-pos/templates/ui"
	"karots-pos/templates/ui/icons"
)

templ UIGallery() {
	@layouts.Base("UI Gallery") {
		<div class="p-6 max-w-5xl mx-auto space-y-10">
			@ui.PageHeader(ui.PageHeaderProps{Title: "UI Gallery", Subtitle: "Phase 0b component library", Crumbs: []ui.Crumb{{Label: "Admin", Href: "/admin"}, {Label: "UI Gallery"}}}) {
				@ui.Button(ui.ButtonProps{Variant: "primary"}) {
					Primary action
				}
			}

			@ui.SectionHeader("Buttons") {
			}
			<div class="flex flex-wrap gap-3">
				@ui.Button(ui.ButtonProps{Variant: "primary"}) { Primary }
				@ui.Button(ui.ButtonProps{Variant: "secondary"}) { Secondary }
				@ui.Button(ui.ButtonProps{Variant: "ghost"}) { Ghost }
				@ui.Button(ui.ButtonProps{Variant: "danger"}) { Danger }
			</div>

			@ui.SectionHeader("Badges") {
			}
			<div class="flex flex-wrap gap-2">
				@ui.Badge("neutral") { Neutral }
				@ui.Badge("ok") { OK }
				@ui.Badge("warn") { Warn }
				@ui.Badge("bad") { Bad }
				@ui.Badge("info") { Info }
				@ui.Badge("accent") { Accent }
			</div>

			@ui.SectionHeader("Form controls") {
			}
			@ui.Card() {
				<div class="grid gap-4 sm:grid-cols-2">
					@ui.Field(ui.FieldProps{Label: "Name", Required: true}) {
						@ui.Input(templ.Attributes{"placeholder": "e.g. Widget"})
					}
					@ui.Field(ui.FieldProps{Label: "Category", Hint: "Pick one"}) {
						@ui.Select([]ui.Option{{Value: "a", Label: "Alpha"}, {Value: "b", Label: "Beta"}}, "b", templ.Attributes{})
					}
					@ui.Field(ui.FieldProps{Label: "With error", Error: "This field is required"}) {
						@ui.Input(templ.Attributes{})
					}
					@ui.Checkbox("Enable feature", templ.Attributes{})
				</div>
			}

			@ui.SectionHeader("Stat tiles") {
			}
			<div class="grid gap-3 sm:grid-cols-3">
				@ui.StatTile(ui.StatTileProps{Label: "Today's sales", Value: "Rs 12,500", Icon: "chart", Area: "reports"})
				@ui.StatTile(ui.StatTileProps{Label: "Items in stock", Value: "1,204", Icon: "boxes", Area: "inventory"})
				@ui.StatTile(ui.StatTileProps{Label: "Open credit", Value: "Rs 3,200", Sub: "8 customers", Icon: "wallet", Area: "money"})
			</div>

			@ui.SectionHeader("Action tiles") {
			}
			<div class="grid gap-3 sm:grid-cols-3">
				@ui.ActionTile(ui.ActionTileProps{Title: "New Sale", Desc: "Start a transaction", Href: "/cashier", Icon: "receipt", Area: "sell"})
				@ui.ActionTile(ui.ActionTileProps{Title: "Add Product", Desc: "Create a catalog item", Href: "/admin/products/form", Icon: "box", Area: "inventory"})
				@ui.ActionTile(ui.ActionTileProps{Title: "Reports", Desc: "Sales & finance", Href: "/admin/reports", Icon: "chart", Area: "reports"})
			</div>

			@ui.SectionHeader("Empty state") {
			}
			@ui.Card() {
				@ui.EmptyState("box", "No products yet", "Add your first product to get started.") {
					@ui.Button(ui.ButtonProps{Variant: "primary"}) { Add product }
				}
			}

			@ui.SectionHeader("Modal") {
			}
			<div>
				@ui.Button(ui.ButtonProps{Variant: "secondary", Attrs: templ.Attributes{"x-data": "", "x-on:click": "$dispatch('open-modal-demo')"}}) {
					Open modal
				}
				@ui.Modal("demo", "Demo modal") {
					<p class="text-sm text-muted">This is a themeable modal. Press Esc, click ✕, or click the backdrop to close.</p>
				}
			</div>

			@ui.SectionHeader("Icons") {
			}
			<div class="grid grid-cols-4 sm:grid-cols-8 gap-4">
				for _, n := range icons.Names() {
					<div class="flex flex-col items-center gap-1 text-muted">
						@icons.Icon(n, "w-6 h-6 text-body")
						<span class="text-[10px]">{ n }</span>
					</div>
				}
			</div>
		</div>
	}
}
```

- [ ] **Step 2: Write the handler**

Create `internal/web/admin_uigallery.go`:
```go
package web

import (
	adminpages "karots-pos/templates/pages/admin"
	"karots-pos/internal/response"

	"github.com/labstack/echo/v4"
)

// UIGallery renders the Phase 0b component gallery for visual QA.
func (h *adminUI) UIGallery(c echo.Context) error {
	return response.Render(c, adminpages.UIGallery())
}
```

(Confirm the full-page render helper name: the codebase uses `response.Render` for full pages and `response.RenderFragment` for fragments — check `internal/response/templ.go` and match the existing admin full-page handlers, e.g. how `admin.Dashboard` renders. If full-page render is named differently, use that name.)

- [ ] **Step 3: Register the route**

In `internal/web/web.go`, in the admin group `ag` (alongside other `ag.GET` routes):
```go
	ag.GET("/ui", admin.UIGallery)
```

- [ ] **Step 4: Generate, build, run, verify visually**

Run:
```bash
make templ && make css && go build -o /tmp/claude-1000/-home-karots-Projects-go-karots-pos/0059bd78-27b3-48e5-8c81-1868ebd3f592/scratchpad/posserver ./cmd/server
# start with .env sourced on :3000, log in as admin, open /admin/ui
```
Verify in a browser (desktop + phone widths): all buttons/badges/forms/tiles/empty-state render with theme colors; every icon shows (no blanks → any blank means that icon's geometry is missing/broken); the modal opens via the button and closes on Esc/✕/backdrop; switching the active theme (Settings → Appearance) recolors the whole gallery.

- [ ] **Step 5: Run the full suite + vet + cross-platform build**

Run:
```bash
go test ./templates/ui/... -v && go vet ./... && make templ && make css && go build ./... && GOOS=windows go build ./cmd/server
```
Expected: all PASS, vet clean, both builds OK.

- [ ] **Step 6: Commit**

```bash
git add templates/pages/admin/uigallery.templ internal/web/admin_uigallery.go internal/web/web.go
git commit -m "feat(ui): /admin/ui component gallery for visual QA

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Final verification (whole plan)

- [ ] `go test ./templates/ui/...` — all PASS (class helpers + render smokes + icon registry).
- [ ] `go vet ./...` — clean.
- [ ] `make templ && make css && go build ./...` — clean. `GOOS=windows go build ./cmd/server` — clean.
- [ ] `/admin/ui` renders every component + every icon; modal open/close works; theme switch recolors the gallery at desktop + phone widths.
- [ ] `cmd/server/enabled_plugins.go` unchanged (core-only); `static/css/tailwind.css` left unstaged; no `*_templ.go` staged.

## Notes for the next plan (not in scope here)

- **Phase 0c — Responsive shells + color-coded browsable nav:** rebuild `templates/layouts/admin.templ` + `cashier.templ` using these components (`PageHeader`, `ActionTile`, `icons.Icon`): inline-expanding colored sidebar (desktop) / bottom-bar + drawer (phone), area-color landing dashboards, removing the section→hub→page indirection. This is where `Theme.Mode` light/dark wiring + migrating the hardcoded `<body>` slate classes onto tokens should also land.
- **Phases 1–5 — page conversions** recompose existing pages onto this library per the design spec rollout.
