package config

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

// LoadDotEnv reads a .env file into the process environment before Load() looks
// for anything.
//
// A shop is shipped a binary and a filled-in .env, and until now nothing read
// that file — the variables had to be exported by hand every start, which is not
// something a shopkeeper is going to do. It looks beside the executable first
// (where the shipped file sits) and then in the working directory (where a
// developer runs from).
//
// A variable already present in the real environment always wins, so a service
// manager, a Docker `-e`, or a one-off `POS_SYSTEM_PIN=… ./karots-pos` still
// overrides the file rather than being silently replaced by it.
//
// Best-effort by design: a missing or unreadable file is not an error, because
// every value it could carry is either optional or reported precisely by Load().
func LoadDotEnv() {
	for _, path := range dotEnvPaths() {
		if loadDotEnvFile(path) {
			return // first one found wins
		}
	}
}

// dotEnvPaths lists where a .env may sit, nearest-to-the-binary first.
func dotEnvPaths() []string {
	var paths []string
	if exe, err := os.Executable(); err == nil {
		// Resolve symlinks so a binary on the PATH still finds the file that was
		// shipped alongside the real executable.
		if resolved, rerr := filepath.EvalSymlinks(exe); rerr == nil {
			exe = resolved
		}
		paths = append(paths, filepath.Join(filepath.Dir(exe), ".env"))
	}
	return append(paths, ".env")
}

// loadDotEnvFile applies one file, reporting whether it was read.
func loadDotEnvFile(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		key, val, ok := parseDotEnvLine(sc.Text())
		if !ok {
			continue
		}
		// Never clobber a variable the caller actually set.
		if _, exists := os.LookupEnv(key); exists {
			continue
		}
		_ = os.Setenv(key, val)
	}
	return sc.Err() == nil
}

// parseDotEnvLine splits one KEY=VALUE line, tolerating a leading `export`,
// blank lines and comments.
//
// It splits on the FIRST `=` only: a DATABASE_URL carries `?sslmode=require&…`,
// and splitting on every `=` would truncate it. Surrounding quotes are stripped,
// but a `#` inside a value is left alone — treating it as a comment would
// quietly corrupt any password containing one.
func parseDotEnvLine(line string) (key, val string, ok bool) {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") {
		return "", "", false
	}
	line = strings.TrimPrefix(line, "export ")

	key, val, found := strings.Cut(line, "=")
	if !found {
		return "", "", false
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return "", "", false
	}
	val = strings.TrimSpace(val)
	if len(val) >= 2 {
		if (val[0] == '"' && val[len(val)-1] == '"') ||
			(val[0] == '\'' && val[len(val)-1] == '\'') {
			val = val[1 : len(val)-1]
		}
	}
	return key, val, true
}
