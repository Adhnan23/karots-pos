package web

import (
	"net/http"
	"time"

	"karots-pos/internal/apperr"
	"karots-pos/internal/backup"
	"karots-pos/internal/features/audit"
	"karots-pos/internal/response"

	"github.com/labstack/echo/v4"
)

// Backup streams a gzipped logical snapshot of all data for download. It runs
// entirely over the app's existing DB connection (see internal/backup) — no
// pg_dump, no psql, no docker — so it works whether Postgres is in a container
// or on a remote VPS, with nothing extra installed on the host.
func (a *adminUI) Backup(c echo.Context) error {
	now := time.Now()
	name := "pos-backup-" + now.Format("20060102-150405") + ".json.gz"
	c.Response().Header().Set(echo.HeaderContentType, "application/gzip")
	c.Response().Header().Set(echo.HeaderContentDisposition, `attachment; filename="`+name+`"`)
	c.Response().WriteHeader(http.StatusOK)

	if err := backup.Dump(c.Request().Context(), a.db, now.Format(time.RFC3339), c.Response().Writer); err != nil {
		// Headers are already sent; we can't switch to a clean error page, but we
		// still surface it server-side and in the audit trail.
		c.Logger().Errorf("backup failed: %v", err)
		return err
	}
	a.s.logAudit(c, audit.ActionBackup, "system", "", "downloaded database backup")
	return nil
}

// Restore replaces all current data with an uploaded backup file. Admin-only and
// guarded by a confirmation in the UI — it TRUNCATEs and reloads every table.
func (a *adminUI) Restore(c echo.Context) error {
	file, err := c.FormFile("backup")
	if err != nil {
		return apperr.BadRequest("choose a backup file (.json.gz) to restore")
	}
	src, err := file.Open()
	if err != nil {
		return apperr.BadRequest("cannot read the uploaded file")
	}
	defer src.Close()

	if err := backup.Restore(c.Request().Context(), a.db, src); err != nil {
		return apperr.BadRequest("restore failed: " + err.Error())
	}

	a.s.logAudit(c, audit.ActionRestore, "system", "", "restored database from "+file.Filename)
	c.Response().Header().Set("HX-Trigger", response.Toast("Database restored — reloading…", "success"))
	return c.NoContent(http.StatusOK)
}
