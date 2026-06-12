package printing

import "testing"

func TestTCPTarget(t *testing.T) {
	cases := []struct {
		in       string
		wantHost string
		wantOK   bool
	}{
		{"tcp://192.168.1.50:9100", "192.168.1.50:9100", true},
		{"tcp://192.168.1.50", "192.168.1.50:9100", true}, // default port appended
		{"  tcp://printer.local:9100 ", "printer.local:9100", true}, // trimmed
		{"POS80", "", false},        // an OS printer name
		{"", "", false},             // system default
		{"tcp://", "", false},       // empty host
		{"http://x:9100", "", false}, // wrong scheme
	}
	for _, c := range cases {
		host, ok := tcpTarget(c.in)
		if ok != c.wantOK || host != c.wantHost {
			t.Errorf("tcpTarget(%q) = (%q, %v), want (%q, %v)", c.in, host, ok, c.wantHost, c.wantOK)
		}
	}
}
