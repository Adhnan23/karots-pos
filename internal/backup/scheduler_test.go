package backup

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRotateKeepsNewest(t *testing.T) {
	dir := t.TempDir()

	// 30 backup files with ascending timestamps (lexical order == chronological).
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var names []string
	for i := range 30 {
		name := filePrefix + base.Add(time.Duration(i)*time.Hour).Format("20060102-150405") + fileSuffix
		names = append(names, name)
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// An unrelated file must be ignored by rotation.
	other := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(other, []byte("keep me"), 0o644); err != nil {
		t.Fatal(err)
	}

	pruned, err := Rotate(dir, 28)
	if err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	if pruned != 2 {
		t.Fatalf("pruned = %d, want 2", pruned)
	}

	// The two oldest are gone; the newest 28 and the unrelated file remain.
	if _, err := os.Stat(filepath.Join(dir, names[0])); !os.IsNotExist(err) {
		t.Errorf("oldest backup %s should have been pruned", names[0])
	}
	if _, err := os.Stat(filepath.Join(dir, names[1])); !os.IsNotExist(err) {
		t.Errorf("second-oldest backup %s should have been pruned", names[1])
	}
	if _, err := os.Stat(filepath.Join(dir, names[2])); err != nil {
		t.Errorf("backup %s should have been kept: %v", names[2], err)
	}
	if _, err := os.Stat(other); err != nil {
		t.Errorf("unrelated file should not be touched: %v", err)
	}

	matches, _ := filepath.Glob(filepath.Join(dir, filePrefix+"*"+fileSuffix))
	if len(matches) != 28 {
		t.Fatalf("remaining backups = %d, want 28", len(matches))
	}
}

func TestRotateNoopWhenUnderLimit(t *testing.T) {
	dir := t.TempDir()
	for i := range 5 {
		name := filePrefix + time.Date(2026, 1, 1, i, 0, 0, 0, time.UTC).Format("20060102-150405") + fileSuffix
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	pruned, err := Rotate(dir, 28)
	if err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	if pruned != 0 {
		t.Fatalf("pruned = %d, want 0 when under the limit", pruned)
	}
}
