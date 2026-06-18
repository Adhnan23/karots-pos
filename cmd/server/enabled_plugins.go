package main

// enabled_plugins.go lists the plugins compiled into THIS build. The committed
// default imports none, producing a core-only binary.
//
// The bootstrapper CLI (cmd/bootstrap) rewrites this file with blank imports of
// the selected plugins, e.g.:
//
//	import (
//		_ "karots-pos/plugins/recharge"
//	)
//
// then builds the per-shop binary, then restores this file. Each imported
// plugin's init() calls plugin.Register, so plugin.All()/SetupAll (and the
// per-plugin migration loop in main.go) pick it up. Keep this file import-only
// so the bootstrapper can rewrite it safely.
