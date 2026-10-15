package core

import (
	"encoding/json"
	"path/filepath"
	"regexp"
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
			a.Tool = "Bash"
			a.Detail = command(execCommand(string(input)))
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
	execCmd   = regexp.MustCompile(`cmd:\s*"((?:[^"\\]|\\.)*)"`)
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

// execCommand pulls the first shell command out of a Codex exec script.
func execCommand(input string) string {
	var script string
	if json.Unmarshal([]byte(input), &script) != nil {
		script = input
	}
	m := execCmd.FindStringSubmatch(script)
	if m == nil {
		return ""
	}
	var c string
	if json.Unmarshal([]byte(`"`+m[1]+`"`), &c) != nil {
		return m[1]
	}
	return c
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
