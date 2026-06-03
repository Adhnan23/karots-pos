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
  ],
  safelist: [
    "text-amber-600",
    "text-emerald-600",
    "text-indigo-600",
    "text-rose-600",
    "text-slate-600",
  ],
  theme: { extend: {} },
  plugins: [],
};
