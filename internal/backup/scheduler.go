package backup

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/jmoiron/sqlx"
)

// filePrefix / fileSuffix bracket the timestamped name used for both the manual
// download (see internal/web/admin_backup.go) and the scheduled files; the
// YYYYMMDD-HHMMSS timestamp makes them sort chronologically by name.
const (
	filePrefix = "pos-backup-"
	fileSuffix = ".json.gz"
)

// WriteSnapshot dumps all data to dir/pos-backup-<timestamp>.json.gz, returning
// the final path. It writes to a temporary file and renames it into place so a
// crash mid-dump never leaves a half-written ".gz" that looks restorable.
func WriteSnapshot(ctx context.Context, db *sqlx.DB, dir string, now time.Time) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create backup dir: %w", err)
	}
	final := filepath.Join(dir, filePrefix+now.Format("20060102-150405")+fileSuffix)
	tmp := final + ".tmp"

	f, err := os.Create(tmp)
	if err != nil {
		return "", fmt.Errorf("create backup file: %w", err)
	}
	if err := Dump(ctx, db, now.Format(time.RFC3339), f); err != nil {
		f.Close()
		os.Remove(tmp)
		return "", err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return "", err
	}
	if err := os.Rename(tmp, final); err != nil {
		os.Remove(tmp)
		return "", fmt.Errorf("finalize backup file: %w", err)
	}
	return final, nil
}

// Rotate deletes the oldest scheduled backups in dir, keeping the newest `keep`.
// Files are matched by the pos-backup-*.json.gz name and ordered by name (the
// embedded timestamp sorts chronologically). keep <= 0 disables pruning.
func Rotate(dir string, keep int) (pruned int, err error) {
	if keep <= 0 {
		return 0, nil
	}
	matches, err := filepath.Glob(filepath.Join(dir, filePrefix+"*"+fileSuffix))
	if err != nil {
		return 0, err
	}
	if len(matches) <= keep {
		return 0, nil
	}
	sort.Strings(matches) // oldest first
	for _, old := range matches[:len(matches)-keep] {
		if rmErr := os.Remove(old); rmErr != nil {
			err = rmErr // report the last failure but keep going
			continue
		}
		pruned++
	}
	return pruned, err
}

// RunScheduler writes a backup immediately, then every `interval`, pruning to the
// newest `keep` files each time. All outcomes are logged; errors never stop the
// loop (a transient backup failure must not take down the POS). It returns when
// ctx is cancelled. Intended to be launched with `go RunScheduler(...)`.
func RunScheduler(ctx context.Context, db *sqlx.DB, dir string, interval time.Duration, keep int) {
	runOnce := func() {
		path, err := WriteSnapshot(ctx, db, dir, time.Now())
		if err != nil {
			log.Printf("auto-backup: snapshot failed: %v", err)
			return
		}
		pruned, err := Rotate(dir, keep)
		if err != nil {
			log.Printf("auto-backup: wrote %s but rotation had an error: %v", path, err)
			return
		}
		log.Printf("auto-backup: wrote %s (pruned %d old, keep %d)", path, pruned, keep)
	}

	runOnce()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			log.Printf("auto-backup: stopping")
			return
		case <-ticker.C:
			runOnce()
		}
	}
}
