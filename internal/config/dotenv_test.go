package config

import (
	"os"
	"testing"
)

// A shop is shipped a binary and a filled-in .env. Nothing read that file, so the
// variables had to be exported by hand on every start — which a shopkeeper is
// never going to do.

func TestParseDotEnvLine(t *testing.T) {
	cases := []struct {
		name, line, key, val string
		ok                   bool
	}{
		{"plain", "SERVER_PORT=3000", "SERVER_PORT", "3000", true},
		{"spaces around =", "  SERVER_PORT = 3000 ", "SERVER_PORT", "3000", true},
		{"export prefix", "export APP_ENV=production", "APP_ENV", "production", true},
		{"double quoted", `JWT_SECRET="a b c"`, "JWT_SECRET", "a b c", true},
		{"single quoted", `JWT_SECRET='a b c'`, "JWT_SECRET", "a b c", true},
		{"empty value", "BACKUP_DIR=", "BACKUP_DIR", "", true},
		{"comment", "# SERVER_PORT=3000", "", "", false},
		{"blank", "   ", "", "", false},
		{"no equals", "JUST_A_WORD", "", "", false},
		{"no key", "=orphan", "", "", false},
		// The connection string is the reason this splits on the FIRST '=' only.
		{
			"url with query params",
			"DATABASE_URL=postgres://u:p@h/db?sslmode=require&channel_binding=require",
			"DATABASE_URL",
			"postgres://u:p@h/db?sslmode=require&channel_binding=require",
			true,
		},
		// A '#' inside a password is not a comment; treating it as one would
		// silently truncate the credential and the shop would just see "can't
		// connect".
		{"hash inside a value", "JWT_SECRET=abc#def", "JWT_SECRET", "abc#def", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			key, val, ok := parseDotEnvLine(tc.line)
			if ok != tc.ok {
				t.Fatalf("ok = %v, want %v", ok, tc.ok)
			}
			if !ok {
				return
			}
			if key != tc.key || val != tc.val {
				t.Errorf("got %q=%q, want %q=%q", key, val, tc.key, tc.val)
			}
		})
	}
}

// A real environment variable must beat the file, so a service manager or a
// one-off POS_SYSTEM_PIN=… override still works.
func TestDotEnvDoesNotClobberTheRealEnvironment(t *testing.T) {
	t.Setenv("KAROTS_TEST_VAR", "from-environment")

	dir := t.TempDir()
	path := dir + "/.env"
	if err := os.WriteFile(path, []byte("KAROTS_TEST_VAR=from-file\nKAROTS_TEST_NEW=from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !loadDotEnvFile(path) {
		t.Fatal("file was not read")
	}
	if got := getEnv("KAROTS_TEST_VAR", ""); got != "from-environment" {
		t.Errorf("the file overwrote a set variable: got %q", got)
	}
	if got := getEnv("KAROTS_TEST_NEW", ""); got != "from-file" {
		t.Errorf("an unset variable was not filled from the file: got %q", got)
	}
}

// A missing file is normal (developers export their own env), never fatal.
func TestDotEnvMissingFileIsNotAnError(t *testing.T) {
	if loadDotEnvFile(t.TempDir() + "/does-not-exist") {
		t.Error("reported success for a file that is not there")
	}
}
