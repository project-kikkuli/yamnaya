package usage

// Transcript is one transcript file: a Claude Code session's own transcript or a Codex rollout.
type Transcript struct {
	Path    string
	Harness string
	Session string
	Size    int64
	MTime   int64
}

// RootTranscripts lists every Claude session transcript (not its subagents) and every Codex rollout.
func RootTranscripts(claudeDir, codexHome string) []Transcript {
	var out []Transcript
	for _, s := range claudeSources(claudeDir) {
		if s.Root {
			out = append(out, Transcript{Path: s.Path, Harness: claudeName, Session: s.Session, Size: s.Size, MTime: s.MTime})
		}
	}
	for _, s := range codexSources(codexHome) {
		out = append(out, Transcript{Path: s.Path, Harness: codexName, Session: s.Session, Size: s.Size, MTime: s.MTime})
	}
	return out
}

// ReadLines calls fn for each complete line after offset and returns the offset past the last one.
func ReadLines(path string, offset int64, fn func(line []byte)) (int64, error) {
	return readLines(path, offset, fn)
}

// Dirs are the Claude config dir and Codex home ym reads transcripts from.
func Dirs() (claudeDir, codexHome string) {
	m := New("")
	return m.ClaudeDir, m.CodexHome
}
