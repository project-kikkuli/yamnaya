package claude

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"regexp"
)

var taskNotice = regexp.MustCompile(`<task-id>([^<]+)</task-id>`)

type backgroundLine struct {
	Type    string `json:"type"`
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

// BackgroundTasks names the background Bash commands and Agent subagents of the transcript's last run that neither finished nor
// were stopped before that run ended, oldest first. Claude writes cost-state records as its process exits, and tasks die with it,
// so a killed notice after those records is the exit's own, and turns after them start a new run.
func BackgroundTasks(path string) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	bash := map[string]string{}
	started := map[string]string{}
	ended := map[string]bool{}
	var order []string
	exited := false
	r := bufio.NewReader(f)
	for {
		line, err := r.ReadBytes('\n')
		var l backgroundLine
		if len(line) > 0 && json.Unmarshal(line, &l) == nil {
			switch l.Type {
			case "cost-state":
				exited = true
			case "user", "assistant":
				if exited {
					exited, order = false, nil
					clear(started)
					clear(ended)
				}
			}
			if !exited && bytes.Contains(line, []byte("<task-notification>")) {
				for _, m := range taskNotice.FindAllSubmatch(line, -1) {
					ended[string(m[1])] = true
				}
			}
			var blocks []backgroundBlock
			json.Unmarshal(l.Message.Content, &blocks)
			for _, b := range blocks {
				if b.Type == "tool_use" && b.Name == "Bash" {
					bash[b.ID] = b.Input.Description
					if bash[b.ID] == "" {
						bash[b.ID] = b.Input.Command
					}
				}
			}
			for _, id := range []string{l.Result.Stopped, l.Result.Killed} {
				if id != "" {
					ended[id] = true
				}
			}
			id, what := "", ""
			switch {
			case l.Result.TaskID != "":
				id, what = l.Result.TaskID, "background Bash "+l.Result.TaskID
				for _, b := range blocks {
					if bash[b.UseID] != "" {
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
		if err != nil {
			break
		}
	}
	var out []string
	for _, id := range order {
		if !ended[id] {
			out = append(out, started[id])
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
