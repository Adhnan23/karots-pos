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
//	-os       target GOOS: linux | windows | darwin   (default: host OS)
//	-arch     target GOARCH                            (default: amd64)
//	-name     output binary base name                 (default: karots-pos)
//	-out      output directory                         (default: dist)
//	-yes      assume yes / non-interactive
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
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
		osFlag      = flag.String("os", runtime.GOOS, "target GOOS: linux | windows | darwin")
		archFlag    = flag.String("arch", "amd64", "target GOARCH")
		nameFlag    = flag.String("name", "karots-pos", "output binary base name")
		outFlag     = flag.String("out", "dist", "output directory")
		yes         = flag.Bool("yes", false, "assume yes / non-interactive")
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

	target, err := chooseOS(*osFlag, *yes)
	if err != nil {
		return err
	}

	fmt.Printf("\nBuilding %q for %s/%s with: %s\n", *nameFlag, target, *archFlag, pluginNames(selected))

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
	build := exec.Command("go", "build", "-ldflags=-s -w", "-o", outBin, "./cmd/server")
	build.Env = append(os.Environ(), "GOOS="+target, "GOARCH="+*archFlag, "CGO_ENABLED=0")
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
		for _, k := range strings.Split(flagVal, ",") {
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

	// Interactive: ask per plugin.
	fmt.Println("Select plugins to compile in (y/N):")
	in := bufio.NewReader(os.Stdin)
	var sel []manifest
	for _, m := range all {
		fmt.Printf("  %s — %s [y/N]: ", m.Name, m.Description)
		line, _ := in.ReadString('\n')
		if ans := strings.ToLower(strings.TrimSpace(line)); ans == "y" || ans == "yes" {
			sel = append(sel, m)
		}
	}
	return sel, nil
}

// chooseOS validates the -os flag or, when interactive, prompts (defaulting to
// the flag value, which itself defaults to the host OS).
func chooseOS(flagVal string, yes bool) (string, error) {
	valid := map[string]bool{"linux": true, "windows": true, "darwin": true}
	def := strings.ToLower(strings.TrimSpace(flagVal))
	if def == "" {
		def = runtime.GOOS
	}
	if !valid[def] {
		return "", fmt.Errorf("unsupported -os %q (linux | windows | darwin)", flagVal)
	}
	// Non-interactive: take the flag (or host) value as-is. An explicit -os was
	// honoured; the host default is also accepted without prompting.
	if yes || flagWasSet("os") {
		return def, nil
	}
	in := bufio.NewReader(os.Stdin)
	fmt.Printf("Target OS [linux/windows/darwin] (default %s): ", def)
	line, _ := in.ReadString('\n')
	ans := strings.ToLower(strings.TrimSpace(line))
	if ans == "" {
		return def, nil
	}
	if !valid[ans] {
		return "", fmt.Errorf("unsupported OS %q", ans)
	}
	return ans, nil
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

// mergeEnv writes the core .env.example plus each selected plugin's env.sample.
func mergeEnv(dst string, sel []manifest) error {
	var b strings.Builder
	if core, err := os.ReadFile(".env.example"); err == nil {
		b.Write(core)
		if !strings.HasSuffix(b.String(), "\n") {
			b.WriteByte('\n')
		}
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
