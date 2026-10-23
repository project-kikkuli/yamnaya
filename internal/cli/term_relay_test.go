package cli

import (
	"bytes"
	"encoding/base64"
	"slices"
	"testing"
)

func TestTermRelayCopiesAndForwardsWhatTheRemoteDraws(t *testing.T) {
	osc := func(text, end string) string {
		return "\x1b]52;c;" + base64.StdEncoding.EncodeToString([]byte(text)) + end
	}
	for _, tc := range []struct {
		name   string
		writes []string
		copies []string
		ports  []string
	}{
		{"a copy ended by BEL", []string{"a" + osc("hello", "\a") + "b"}, []string{"hello"}, nil},
		{"a copy ended by ST split across writes", []string{"x\x1b]5", "2;;" + base64.StdEncoding.EncodeToString([]byte("two"))[:2], base64.StdEncoding.EncodeToString([]byte("two"))[2:] + "\x1b\\y"}, []string{"two"}, nil},
		{"two copies in one write", []string{osc("one", "\a") + osc("two", "\a")}, []string{"one", "two"}, nil},
		{"a clipboard query sets nothing", []string{"\x1b]52;c;?\a"}, nil, nil},
		{"links to the remote's localhost", []string{"see http://localhost:3000/x and http://127.0.0.1:8080 and http://localhost:3000/y"}, nil, []string{"3000", "8080"}},
		{"a link split across writes", []string{"open http://local", "host:5173/ now"}, nil, []string{"5173"}},
		{"links elsewhere forward nothing", []string{"https://example.com:8443/a http://10.0.0.1:80"}, nil, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			var copies, ports []string
			r := &termRelay{out: &out, copy: func(b []byte) { copies = append(copies, string(b)) }, forward: func(p string) { ports = append(ports, p) }}
			var want string
			for _, w := range tc.writes {
				if n, err := r.Write([]byte(w)); n != len(w) || err != nil {
					t.Fatalf("Write = %d, %v", n, err)
				}
				want += w
			}
			if out.String() != want {
				t.Errorf("terminal got %q, want %q unchanged", out.String(), want)
			}
			if !slices.Equal(copies, tc.copies) {
				t.Errorf("copied %q, want %q", copies, tc.copies)
			}
			if !slices.Equal(ports, tc.ports) {
				t.Errorf("forwarded %q, want %q", ports, tc.ports)
			}
		})
	}
}

func TestCopiedBriefLinksOpenThroughThisMachinesServe(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"see http://127.0.0.1:7477/b/mgr1 now", "see http://127.0.0.1:7477/b/vm.example/mgr1 now"},
		{"http://localhost:7477/b/goal3?v=2", "http://localhost:7477/b/vm.example/goal3?v=2"},
		{"http://127.0.0.1:7477/b/vm.example/mgr1", "http://127.0.0.1:7477/b/vm.example/mgr1"},
		{"http://127.0.0.1:3000/b/mgr1", "http://127.0.0.1:3000/b/mgr1"},
	} {
		if got := string(hostBriefLinks([]byte(tc.in), "vm.example")); got != tc.want {
			t.Errorf("hostBriefLinks(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
