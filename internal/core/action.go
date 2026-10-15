package core

import (
	"encoding/json"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Describe renders a tool call as a short action; detail is the call's most telling argument.
func Describe(tool string, input json.RawMessage, cwd string, at time.Time) Action {
	var args map[string]any
	_ = json.Unmarshal(input, &args)
	str := func(keys ...string) string {
		for _, k := range keys {
			if v, ok := args[k].(string); ok && v != "" {
				return v
			}
			if v, ok := args[k].([]any); ok && len(v) > 0 {
				parts := make([]string, 0, len(v))
				for _, p := range v {
					if s, ok := p.(string); ok {
						parts = append(parts, s)
					}
				}
				return strings.Join(parts, " ")
			}
		}
		return ""
	}
	a := Action{Tool: tool, At: at}
	switch {
	case strings.HasPrefix(tool, "mcp__"):
		parts := strings.SplitN(strings.TrimPrefix(tool, "mcp__"), "__", 2)
		a.Tool = strings.Join(parts, ".")
		a.Detail = str("query", "url", "title", "path", "name", "id")
	default:
		switch tool {
		case "Bash", "exec_command", "shell", "local_shell":
			a.Tool = "Bash"
			a.Detail = command(str("command", "cmd"))
		case "Edit", "Write", "Read", "MultiEdit", "NotebookEdit":
			a.Detail = Rel(str("file_path", "notebook_path", "path"), cwd)
		case "Grep", "Glob":
			a.Detail = str("pattern")
		case "Agent", "Task":
			a.Detail = str("description", "subagent_type")
		case "WebFetch":
			a.Detail = str("url")
		case "WebSearch":
			a.Detail = str("query")
		case "Skill":
			a.Detail = str("skill")
		case "SendMessage":
			a.Detail = "to " + str("to")
		case "apply_patch":
			a.Tool = "Edit"
			a.Detail = strings.Join(patchFiles(string(input), cwd), ", ")
		case "exec":
			a = describeExec(string(input), cwd, at)
		default:
			a.Detail = str("description", "command", "cmd", "file_path", "path", "query", "url", "prompt")
		}
	}
	a.Detail = oneLine(a.Detail)
	return a
}

// command is the first line of a shell command with any leading cd stripped.
func command(c string) string {
	c = strings.TrimSpace(c)
	if i := strings.IndexByte(c, '\n'); i >= 0 {
		c = c[:i]
	}
	for strings.HasPrefix(c, "cd ") {
		i := strings.Index(c, "&&")
		if i < 0 {
			break
		}
		c = strings.TrimSpace(c[i+2:])
	}
	return c
}

var (
	patchFile = regexp.MustCompile(`\*\*\* (?:Update|Add|Delete) File: ([^\n\\"]+)`)
	execCall  = regexp.MustCompile(`tools\.(\w+)\(`)
	execKey   = regexp.MustCompile(`\b(\w+)"?\s*[:=]\s*(?:\[\s*)*["'\x60]`)
)

func patchFiles(input, cwd string) []string {
	var out []string
	for _, m := range patchFile.FindAllStringSubmatch(input, -1) {
		out = append(out, Rel(strings.TrimSpace(m[1]), cwd))
	}
	return out
}

// PatchFiles lists the absolute paths an apply_patch input touches.
func PatchFiles(input, cwd string) []string {
	var out []string
	for _, m := range patchFile.FindAllStringSubmatch(input, -1) {
		p := strings.TrimSpace(m[1])
		if !filepath.IsAbs(p) && cwd != "" {
			p = filepath.Join(cwd, p)
		}
		out = append(out, p)
	}
	return out
}

// describeExec names what a Codex exec script does by its first tool call, and its shell command when it runs one.
func describeExec(input, cwd string, at time.Time) Action {
	var script string
	if json.Unmarshal([]byte(input), &script) != nil {
		script = input
	}
	calls := execCall.FindAllStringSubmatchIndex(script, -1)
	pick := -1
	for i, c := range calls {
		if script[c[2]:c[3]] != "write_stdin" {
			pick = i
			break
		}
	}
	if pick < 0 {
		if len(calls) > 0 {
			return Action{Tool: "Bash", Detail: "waiting on a running command", At: at}
		}
		return Action{Tool: "exec", Detail: oneLine(command(script)), At: at}
	}
	name, args := script[calls[pick][2]:calls[pick][3]], script[calls[pick][1]:]
	switch name {
	case "exec_command":
		c := jsString(args, "cmd")
		if c == "" {
			c = jsString(script, "cmd", "cmds", "command")
		}
		return Action{Tool: "Bash", Detail: oneLine(command(c)), At: at}
	case "apply_patch":
		return Action{Tool: "Edit", Detail: oneLine(strings.Join(patchFiles(script, cwd), ", ")), At: at}
	}
	a := Describe(name, nil, cwd, at)
	a.Detail = oneLine(jsString(args, "title", "description", "query", "q", "ref_id", "url", "id", "target", "path", "message", "code"))
	if a.Detail == "" {
		a.Detail = oneLine(command(script))
	}
	return a
}

// jsString is the first string literal assigned to one of keys in JavaScript source, unescaped.
func jsString(src string, keys ...string) string {
	for _, k := range keys {
		for _, m := range execKey.FindAllStringSubmatchIndex(src, -1) {
			if src[m[2]:m[3]] != k {
				continue
			}
			v, _, ok := jsLiteral(src[m[1]-1:])
			if strings.Count(src[m[0]:m[1]], "[") > 1 {
				v, ok = lastInRow(src[m[1]-1:])
			}
			if ok {
				return v
			}
		}
	}
	return ""
}

// lastInRow is the last string of the array row starting at src, so ["label", "cmd"] gives the command.
func lastInRow(src string) (string, bool) {
	v, n, ok := jsLiteral(src)
	for ok {
		rest := strings.TrimLeft(src[n:], " \t\n,")
		if rest == "" || !strings.ContainsRune(`"'`+"`", rune(rest[0])) {
			break
		}
		next, m, more := jsLiteral(rest)
		if !more {
			break
		}
		v, src, n = next, rest, m
	}
	return v, ok
}

// jsLiteral reads the quoted string at the start of src and how many bytes it spans.
func jsLiteral(src string) (string, int, bool) {
	q := src[0]
	var b strings.Builder
	for i := 1; i < len(src); i++ {
		c := src[i]
		switch {
		case c == q:
			return b.String(), i + 1, true
		case c == '\\' && i+1 < len(src):
			i++
			switch e := src[i]; e {
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			case 'u':
				if i+4 < len(src) {
					if r, err := strconv.ParseUint(src[i+1:i+5], 16, 32); err == nil {
						b.WriteRune(rune(r))
						i += 4
						continue
					}
				}
				b.WriteByte(e)
			default:
				b.WriteByte(e)
			}
		default:
			b.WriteByte(c)
		}
	}
	return "", 0, false
}

// Rel shows path relative to dir when it lies inside it.
func Rel(path, dir string) string {
	if dir != "" && strings.HasPrefix(path, dir+"/") {
		return path[len(dir)+1:]
	}
	return path
}

// TrailLen caps how many recent actions Facts keeps.
const TrailLen = 200

// Did records one tool call and the files it wrote.
func (f *Facts) Did(a Action, wrote ...string) {
	f.Doing = a
	if f.Tools == nil {
		f.Tools = map[string]int{}
	}
	f.Tools[a.Tool]++
	f.Trail = append(f.Trail, a)
	if len(f.Trail) > TrailLen {
		f.Trail = append([]Action(nil), f.Trail[len(f.Trail)-TrailLen:]...)
	}
	for _, w := range wrote {
		if w != "" && !contains(f.Files, w) {
			f.Files = append(f.Files, w)
		}
	}
}

// Opened records a pull request once.
func (f *Facts) Opened(pr PR) {
	for _, p := range f.PRs {
		if p.URL == pr.URL {
			return
		}
	}
	f.PRs = append(f.PRs, pr)
}

var prURL = regexp.MustCompile(`https://github\.com/([^/\s]+/[^/\s]+)/pull/(\d+)`)

// PRsIn finds GitHub pull request links in text.
func PRsIn(text string) []PR {
	var out []PR
	for _, m := range prURL.FindAllStringSubmatch(text, -1) {
		n := 0
		for _, c := range m[2] {
			n = n*10 + int(c-'0')
		}
		out = append(out, PR{URL: m[0], Repo: m[1], Number: n})
	}
	return out
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}
