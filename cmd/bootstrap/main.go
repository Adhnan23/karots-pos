// Command bootstrap builds a per-shop POS binary with a chosen set of plugins
// compiled in. It discovers plugins from plugins/*/plugin.json, rewrites
// cmd/server/enabled_plugins.go with blank imports of the selected plugins,
// generates templates + stylesheet, cross-compiles a static binary, and merges
// each plugin's env.sample into the shop's .env.sample. The enabled_plugins.go
// file is always restored to its original (core-only) contents afterwards.
//
// Usage:
//
//	go run ./cmd/bootstrap                         # interactive
//	go run ./cmd/bootstrap -plugins recharge -os windows -name acme-pos
//	go run ./cmd/bootstrap -plugins all -os linux  # all discovered plugins
//
// Flags:
//
//	-plugins  comma-separated plugin keys, or "all" (omit for interactive)
//	-os       target GOOS: linux | windows | darwin | freebsd  (default: host OS)
//	-arch     target GOARCH: amd64 | arm64                      (default: amd64)
//	-name     output binary base name                 (default: karots-pos)
//	-out      output directory                         (default: dist)
//	-yes      assume yes / non-interactive
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"karots-pos/internal/support"

	"golang.org/x/crypto/bcrypt"

	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"
)

// manifest mirrors plugins/<key>/plugin.json.
type manifest struct {
	Key         string `json:"key"`
	Name        string `json:"name"`
	Import      string `json:"import"`
	Version     string `json:"version"`
	Description string `json:"description"`
	EnvSample   string `json:"env_sample"`

	dir string // directory the manifest was read from
}

const enabledPluginsPath = "cmd/server/enabled_plugins.go"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "bootstrap: "+err.Error())
		os.Exit(1)
	}
}

func run() error {
	var (
		pluginsFlag = flag.String("plugins", "", `comma-separated plugin keys, or "all" (omit for interactive)`)
		osFlag      = flag.String("os", runtime.GOOS, "target GOOS: linux | windows | darwin | freebsd")
		archFlag    = flag.String("arch", "amd64", "target GOARCH: amd64 | arm64")
		nameFlag    = flag.String("name", "karots-pos", "output binary base name")
		outFlag     = flag.String("out", "dist", "output directory")
		yes         = flag.Bool("yes", false, "assume yes / non-interactive")
		// The developer's master key for deriving each shop's support PIN. Read
		// from the environment rather than a flag so it never lands in a shell
		// history or a build log. Without it every binary ever shipped shares one
		// support credential, so the build warns loudly.
		supportSecret = os.Getenv("POS_SUPPORT_SECRET")
	)
	flag.Parse()

	if _, err := os.Stat("go.mod"); err != nil {
		return fmt.Errorf("run from the repository root (go.mod not found)")
	}

	all, err := discover()
	if err != nil {
		return err
	}
	if len(all) == 0 {
		fmt.Println("No plugins found under plugins/*/plugin.json — building core only.")
	}

	selected, err := selectPlugins(all, *pluginsFlag, *yes)
	if err != nil {
		return err
	}

	target, arch, err := chooseTarget(*osFlag, *archFlag, *yes)
	if err != nil {
		return err
	}

	headStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("63"))
	keyStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("213"))
	fmt.Printf("\n%s\n  binary  %s\n  target  %s/%s\n  plugins %s\n\n",
		headStyle.Render("Building per-shop POS"),
		keyStyle.Render(*nameFlag), target, arch, keyStyle.Render(pluginNames(selected)))

	// Rewrite enabled_plugins.go, always restoring it afterwards.
	original, err := os.ReadFile(enabledPluginsPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", enabledPluginsPath, err)
	}
	if err := os.WriteFile(enabledPluginsPath, renderEnabledPlugins(selected), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", enabledPluginsPath, err)
	}
	defer func() {
		if werr := os.WriteFile(enabledPluginsPath, original, 0o644); werr != nil {
			fmt.Fprintf(os.Stderr, "WARNING: failed to restore %s: %v\n", enabledPluginsPath, werr)
		}
	}()

	if err := os.MkdirAll(*outFlag, 0o755); err != nil {
		return err
	}

	// 1) templates → _templ.go
	if err := sh("templ", "generate"); err != nil {
		return fmt.Errorf("templ generate: %w", err)
	}
	// 2) stylesheet (scans plugin templates via tailwind.config.js content globs)
	if err := sh("npx", "-y", "tailwindcss@3", "-c", "tailwind.config.js",
		"-i", "static/css/tailwind.input.css", "-o", "static/css/tailwind.css", "--minify"); err != nil {
		return fmt.Errorf("tailwind css: %w", err)
	}
	// 3) static, self-contained binary
	bin := *nameFlag
	if target == "windows" {
		bin += ".exe"
	}
	outBin := filepath.Join(*outFlag, bin)

	// Derive this shop's support credential HERE, and bake in only the result.
	//
	// The master secret must never reach the binary: Go records the whole
	// -ldflags line in the build metadata, so `go version -m` on any shipped
	// binary would print it and hand the holder every other shop's PIN. What goes
	// in is the install id and a bcrypt HASH of this shop's PIN — useless against
	// any other install, and not even reversible into this one's.
	ldflags := "-s -w"
	var shopInstallID, shopPIN string
	if supportSecret != "" {
		id, ierr := support.NewInstallID()
		if ierr != nil {
			return fmt.Errorf("generate install id: %w", ierr)
		}
		shopInstallID, shopPIN = id, support.DerivePIN(supportSecret, id)
		hash, herr := bcrypt.GenerateFromPassword([]byte(shopPIN), bcrypt.DefaultCost)
		if herr != nil {
			return fmt.Errorf("hash support pin: %w", herr)
		}
		ldflags += " -X main.installIDBaked=" + id + " -X main.supportHash=" + string(hash)
	} else {
		fmt.Println("! POS_SUPPORT_SECRET is not set — this build falls back to the fixed")
		fmt.Println("  support PIN shared by every bare build. Set it so this shop gets its own.")
	}
	build := exec.Command("go", "build", "-ldflags="+ldflags, "-o", outBin, "./cmd/server")
	build.Env = append(os.Environ(), "GOOS="+target, "GOARCH="+arch, "CGO_ENABLED=0")
	build.Stdout, build.Stderr = os.Stdout, os.Stderr
	fmt.Printf("→ go build %s\n", outBin)
	if err := build.Run(); err != nil {
		return fmt.Errorf("go build: %w", err)
	}

	// 4) merge env samples
	envPath := filepath.Join(*outFlag, ".env.sample")
	if err := mergeEnv(envPath, selected); err != nil {
		return fmt.Errorf("merge env: %w", err)
	}

	fmt.Printf("\n✓ Built %s\n✓ Wrote %s\n  Ship the binary + a filled-in .env (rename .env.sample).\n", outBin, envPath)
	if shopInstallID != "" {
		// Printed once, here, because it is the only moment both halves exist in
		// one place. Losing it is not fatal — the shop can read the install id off
		// their console and `make support-pin` recomputes the same PIN.
		fmt.Printf("\n  support login   0000000001 / %s\n  install id      %s\n",
			shopPIN, shopInstallID)
		fmt.Printf("  recover later   make support-pin ID=%s\n", shopInstallID)
	}
	return nil
}

// discover reads every plugins/*/plugin.json into a manifest, sorted by key.
func discover() ([]manifest, error) {
	matches, err := filepath.Glob("plugins/*/plugin.json")
	if err != nil {
		return nil, err
	}
	var out []manifest
	for _, p := range matches {
		raw, err := os.ReadFile(p)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", p, err)
		}
		var m manifest
		if err := json.Unmarshal(raw, &m); err != nil {
			return nil, fmt.Errorf("parse %s: %w", p, err)
		}
		if m.Key == "" || m.Import == "" {
			return nil, fmt.Errorf("%s: key and import are required", p)
		}
		m.dir = filepath.Dir(p)
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

// selectPlugins resolves the -plugins flag or prompts interactively.
func selectPlugins(all []manifest, flagVal string, yes bool) ([]manifest, error) {
	byKey := map[string]manifest{}
	for _, m := range all {
		byKey[m.Key] = m
	}

	if strings.TrimSpace(flagVal) != "" {
		if strings.EqualFold(strings.TrimSpace(flagVal), "all") {
			return all, nil
		}
		var sel []manifest
		for k := range strings.SplitSeq(flagVal, ",") {
			k = strings.TrimSpace(k)
			if k == "" {
				continue
			}
			m, ok := byKey[k]
			if !ok {
				return nil, fmt.Errorf("unknown plugin %q (available: %s)", k, keys(all))
			}
			sel = append(sel, m)
		}
		return sel, nil
	}

	if yes || len(all) == 0 {
		return nil, nil // core-only
	}

	// Interactive: a single checkbox multi-select. Each option shows the plugin
	// name (bold) above a dimmed one-line description, so the list reads as a
	// scannable menu rather than a wall of same-coloured y/N prompts.
	nameStyle := lipgloss.NewStyle().Bold(true)
	descStyle := lipgloss.NewStyle().Faint(true)
	opts := make([]huh.Option[string], 0, len(all))
	for _, m := range all {
		label := nameStyle.Render(m.Name) + "\n  " + descStyle.Render(oneLine(m.Description))
		opts = append(opts, huh.NewOption(label, m.Key))
	}

	var chosen []string
	form := huh.NewForm(huh.NewGroup(
		huh.NewMultiSelect[string]().
			Title("Plugins").
			Description("Space to toggle · ↑/↓ to move · Enter to confirm. Leave all unchecked for a core-only build.").
			Options(opts...).
			Value(&chosen),
	)).WithTheme(huh.ThemeCharm())
	if err := form.Run(); err != nil {
		if errors.Is(err, huh.ErrUserAborted) {
			return nil, fmt.Errorf("cancelled")
		}
		return nil, err
	}

	var sel []manifest
	for _, k := range chosen {
		sel = append(sel, byKey[k])
	}
	return sel, nil
}

// oneLine collapses a manifest description to a single, length-capped line so the
// plugin menu stays tidy even for plugins with a paragraph-long description.
func oneLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if i := strings.IndexAny(s, ".;"); i > 0 && i < 90 {
		s = s[:i]
	}
	const max = 88
	if len(s) > max {
		s = strings.TrimSpace(s[:max]) + "…"
	}
	return s
}

// chooseTarget resolves the build target GOOS/GOARCH. It validates the -os/-arch
// flags, and for any not explicitly set it prompts with a select (OS and
// architecture as two separate lists). Defaults: host OS, amd64.
func chooseTarget(osFlag, archFlag string, yes bool) (string, string, error) {
	validOS := map[string]bool{"linux": true, "windows": true, "darwin": true, "freebsd": true}
	validArch := map[string]bool{"amd64": true, "arm64": true}

	goos := strings.ToLower(strings.TrimSpace(osFlag))
	if goos == "" {
		goos = runtime.GOOS
	}
	if !validOS[goos] {
		return "", "", fmt.Errorf("unsupported -os %q (linux | windows | darwin | freebsd)", osFlag)
	}
	goarch := strings.ToLower(strings.TrimSpace(archFlag))
	if goarch == "" {
		goarch = "amd64"
	}
	if !validArch[goarch] {
		return "", "", fmt.Errorf("unsupported -arch %q (amd64 | arm64)", archFlag)
	}

	if yes {
		return goos, goarch, nil
	}

	// Prompt only for the parts not pinned by an explicit flag.
	var fields []huh.Field
	if !flagWasSet("os") {
		fields = append(fields, huh.NewSelect[string]().
			Title("Operating system").
			Description("Where will this shop's binary run?").
			Options(
				huh.NewOption("Linux", "linux"),
				huh.NewOption("Windows", "windows"),
				huh.NewOption("macOS", "darwin"),
				huh.NewOption("FreeBSD", "freebsd"),
			).
			Value(&goos))
	}
	if !flagWasSet("arch") {
		fields = append(fields, huh.NewSelect[string]().
			Title("Architecture").
			Description("amd64 = Intel/AMD x86-64 · arm64 = ARM (Apple Silicon, Raspberry Pi, ARM servers)").
			Options(
				huh.NewOption("x86-64  (amd64)", "amd64"),
				huh.NewOption("ARM 64-bit  (arm64)", "arm64"),
			).
			Value(&goarch))
	}
	if len(fields) > 0 {
		if err := huh.NewForm(huh.NewGroup(fields...)).WithTheme(huh.ThemeCharm()).Run(); err != nil {
			if errors.Is(err, huh.ErrUserAborted) {
				return "", "", fmt.Errorf("cancelled")
			}
			return "", "", err
		}
	}
	return goos, goarch, nil
}

// flagWasSet reports whether the named flag was explicitly passed.
func flagWasSet(name string) bool {
	set := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == name {
			set = true
		}
	})
	return set
}

// renderEnabledPlugins produces the cmd/server/enabled_plugins.go contents for a
// selection. With no plugins it matches the committed core-only default shape.
func renderEnabledPlugins(sel []manifest) []byte {
	var b strings.Builder
	b.WriteString("package main\n\n")
	b.WriteString("// Code generated by cmd/bootstrap. DO NOT EDIT.\n")
	b.WriteString("// Lists the plugins compiled into this per-shop build.\n\n")
	if len(sel) == 0 {
		b.WriteString("// core-only build (no plugins selected)\n")
		return []byte(b.String())
	}
	b.WriteString("import (\n")
	for _, m := range sel {
		fmt.Fprintf(&b, "\t_ %q // %s\n", m.Import, m.Name)
	}
	b.WriteString(")\n")
	return []byte(b.String())
}

// mergeEnv writes the shop env template plus each selected plugin's env.sample.
func mergeEnv(dst string, sel []manifest) error {
	var b strings.Builder
	// The shop's template, not the developer's: .env.example carries the master
	// key line, and this file lands on the shop's machine.
	core, err := os.ReadFile(".env.production.example")
	if err != nil {
		return fmt.Errorf("read .env.production.example: %w", err)
	}
	b.Write(core)
	if !strings.HasSuffix(b.String(), "\n") {
		b.WriteByte('\n')
	}
	for _, m := range sel {
		name := m.EnvSample
		if name == "" {
			name = "env.sample"
		}
		raw, err := os.ReadFile(filepath.Join(m.dir, name))
		if err != nil {
			continue // plugin has no env sample
		}
		fmt.Fprintf(&b, "\n# ---- plugin: %s ----\n", m.Name)
		b.Write(raw)
		if !strings.HasSuffix(string(raw), "\n") {
			b.WriteByte('\n')
		}
	}
	return os.WriteFile(dst, []byte(b.String()), 0o644)
}

func sh(name string, args ...string) error {
	fmt.Printf("→ %s %s\n", name, strings.Join(args, " "))
	c := exec.Command(name, args...)
	c.Stdout, c.Stderr = os.Stdout, os.Stderr
	return c.Run()
}

func pluginNames(sel []manifest) string {
	if len(sel) == 0 {
		return "core only"
	}
	var names []string
	for _, m := range sel {
		names = append(names, m.Name)
	}
	return strings.Join(names, ", ")
}

func keys(all []manifest) string {
	var ks []string
	for _, m := range all {
		ks = append(ks, m.Key)
	}
	return strings.Join(ks, ", ")
}
