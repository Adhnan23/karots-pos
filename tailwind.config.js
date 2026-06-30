/** @type {import('tailwindcss').Config} */
// Tailwind v3 build config. Replaces the runtime Play CDN with a pre-built,
// minified stylesheet (static/css/tailwind.css) compiled at `make css`.
// `content` scans .templ sources, the generated/helper .go files (which hold
// class strings returned by Go helpers like overShortClass), and app.js (Alpine
// x-bind:class strings). `safelist` covers classes built by string concat that
// Tailwind can't see literally — notably statCard's `text-<color>-600`.
module.exports = {
  darkMode: "class",
  content: [
    "./templates/**/*.templ",
    "./templates/**/*.go",
    "./static/js/**/*.js",
    // Plugin UI (compiled in per-shop by cmd/bootstrap) contributes its own
    // class strings; scan plugin .templ sources and generated/helper .go too.
    "./plugins/**/*.templ",
    "./plugins/**/*.go",
  ],
  safelist: [
    "text-amber-600",
    "text-emerald-600",
    "text-indigo-600",
    "text-rose-600",
    "text-slate-600",
  ],
  theme: {
    extend: {
      colors: {
        surface:   "var(--surface)",
        "surface-2": "var(--surface-2)",
        line:      "var(--border)",
        body:      "var(--text)",
        muted:     "var(--text-muted)",
        accent: {
          DEFAULT: "var(--accent)",
          fg:      "var(--accent-fg)",
          weak:    "var(--accent-weak)",
        },
        ok:    "var(--success)",
        warn:  "var(--warning)",
        bad:   "var(--danger)",
        info:  "var(--info)",
        area: {
          sell:       "var(--area-sell)",
          inventory:  "var(--area-inventory)",
          purchasing: "var(--area-purchasing)",
          money:      "var(--area-money)",
          reports:    "var(--area-reports)",
          setup:      "var(--area-setup)",
        },
      },
      borderRadius: { token: "var(--radius)" },
      spacing: { token: "var(--space)" },
      height: { control: "var(--control-h)" },
      minHeight: { control: "var(--control-h)" },
    },
  },
  plugins: [],
};
