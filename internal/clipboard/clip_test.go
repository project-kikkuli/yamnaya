package clipboard

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"
)

func TestPickUsesPbcopyOnlyOnALocalMacThatHasIt(t *testing.T) {
	for _, tc := range []struct {
		name   string
		goos   string
		env    map[string]string
		pbcopy bool
		want   Way
	}{
		{"local mac", "darwin", nil, true, Pbcopy},
		{"mac over ssh with a tty", "darwin", map[string]string{"SSH_TTY": "/dev/ttys004"}, true, OSC52},
		{"mac over ssh without a tty", "darwin", map[string]string{"SSH_CONNECTION": "10.0.0.2 52000 10.0.0.3 22"}, true, OSC52},
		{"mac without pbcopy", "darwin", nil, false, OSC52},
		{"linux", "linux", nil, true, OSC52},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := Env{GOOS: tc.goos, Getenv: func(k string) string { return tc.env[k] }, LookPath: func(string) (string, error) {
				if tc.pbcopy {
					return "/usr/bin/pbcopy", nil
				}
				return "", errors.New("not found")
			}}
			if got := Pick(e); got != tc.want {
				t.Fatalf("Pick = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestTheOSC52SequenceCarriesTheTextExactly(t *testing.T) {
	for _, text := range []string{
		"plain",
		"say \"copy & paste\" isn't <broken>\n",
		"# goal33: brief\n\n## Assessment\n\nemoji ✓ and — dashes\n",
		strings.Repeat("a long line ", 2000),
	} {
		seq := Sequence(text)
		body, ok := strings.CutPrefix(seq, "\x1b]52;c;")
		body, end := strings.CutSuffix(body, "\a")
		if !ok || !end {
			t.Fatalf("sequence %q is not an OSC 52 clipboard write", seq[:min(len(seq), 20)])
		}
		got, err := base64.StdEncoding.DecodeString(body)
		if err != nil || string(got) != text {
			t.Fatalf("decoded %q, %v; want %q", got, err, text)
		}
	}
}

func TestCleanTextHasNoTrailingSpaceAndOneFinalNewline(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"one", "one\n"},
		{"\n\nheading  \n\nbody\t\n\n\n", "heading\n\nbody\n"},
		{"crlf line\r\nnext\r\n", "crlf line\nnext\n"},
		{"  indented stays\n", "  indented stays\n"},
	} {
		if got := Clean(tc.in); got != tc.want {
			t.Fatalf("Clean(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
