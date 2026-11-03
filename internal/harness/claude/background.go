package claude

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"regexp"
	"strings"
)

var taskNotice = regexp.MustCompile(`<task-id>([^<]+)</task-id>`)

type backgroundLine struct {
	Message struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`
	Result struct {
		TaskID      string `json:"backgroundTaskId"`
		Async       bool   `json:"isAsync"`
		AgentID     string `json:"agentId"`
		Description string `json:"description"`
		Stopped     string `json:"task_id"`
		Killed      string `json:"shell_id"`
	} `json:"toolUseResult"`
}

type backgroundBlock struct {
	Type  string `json:"type"`
	ID    string `json:"id"`
	Name  string `json:"name"`
	UseID string `json:"tool_use_id"`
	Input struct {
		Command     string `json:"command"`
		Description string `json:"description"`
	} `json:"input"`
}

// BackgroundTasks names the background Bash commands and Agent subagents the transcript at path started and neither saw
// finish nor stopped, oldest first.
func BackgroundTasks(path string) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	bash := map[string]string{}
	started := map[string]string{}
	var order []string
	ended := map[string]bool{}
	r := bufio.NewReader(f)
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 {
			if bytes.Contains(line, []byte("<task-notification>")) {
				for _, m := range taskNotice.FindAllSubmatch(line, -1) {
					ended[string(m[1])] = true
				}
			}
			bashUse := bytes.Contains(line, []byte(`"name":"Bash"`))
			launched := bytes.Contains(line, []byte(`"backgroundTaskId"`)) || bytes.Contains(line, []byte(`"isAsync":true`)) || bytes.Contains(line, []byte(`"task_id"`)) || bytes.Contains(line, []byte(`"shell_id"`))
			if bashUse || launched {
				var l backgroundLine
				if json.Unmarshal(line, &l) == nil {
					var blocks []backgroundBlock
					json.Unmarshal(l.Message.Content, &blocks)
					for _, b := range blocks {
						if b.Type == "tool_use" && b.Name == "Bash" {
							what := b.Input.Description
							if what == "" {
								what = b.Input.Command
							}
							bash[b.ID] = what
						}
					}
					for _, stopped := range []string{l.Result.Stopped, l.Result.Killed} {
						if stopped != "" {
							ended[stopped] = true
						}
					}
					id, what := "", ""
					switch {
					case l.Result.TaskID != "":
						id, what = l.Result.TaskID, "background Bash "+l.Result.TaskID
						for _, b := range blocks {
							if b.Type == "tool_result" && bash[b.UseID] != "" {
								what += ": " + brief(bash[b.UseID])
							}
						}
					case l.Result.Async && l.Result.AgentID != "":
						id, what = l.Result.AgentID, "background Agent "+l.Result.AgentID
						if l.Result.Description != "" {
							what += ": " + brief(l.Result.Description)
						}
					}
					if id != "" {
						if _, seen := started[id]; !seen {
							order = append(order, id)
						}
						started[id] = what
					}
				}
			}
		}
		if err != nil {
			break
		}
	}
	var out []string
	for _, id := range order {
		if !ended[id] {
			out = append(out, strings.TrimSpace(started[id]))
		}
	}
	return out
}

func brief(s string) string {
	s = firstLine(s)
	if r := []rune(s); len(r) > 100 {
		return string(r[:100]) + "…"
	}
	return s
}
