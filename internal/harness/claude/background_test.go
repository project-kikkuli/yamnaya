package claude

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestBackgroundTasksListOnlyWhatTheLastRunCutOff(t *testing.T) {
	bash := func(id string) string {
		return `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"use-` + id + `","name":"Bash","input":{"command":"run ` + id + `"}}]}}` + "\n" +
			`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"use-` + id + `"}]},"toolUseResult":{"backgroundTaskId":"` + id + `"}}`
	}
	agent := `{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"use-a"}]},"toolUseResult":{"isAsync":true,"status":"async_launched","agentId":"a1","description":"Slack sweep"}}`
	notice := func(id, status string) string {
		return `{"type":"queue-operation","operation":"enqueue","content":"<task-notification>\n<task-id>` + id + `</task-id>\n<status>` + status + `</status>\n</task-notification>"}`
	}
	exit := `{"type":"cost-state","totalDuration":1,"startTime":1}`
	turn := `{"type":"user","message":{"content":"ym switched this conversation"}}`
	for _, c := range []struct {
		name  string
		lines []string
		want  []string
	}{
		{"running bash and agent", []string{bash("b1"), agent, exit}, []string{"background Bash b1: run b1", "background Agent a1: Slack sweep"}},
		{"completed before the stop", []string{bash("b1"), notice("b1", "completed"), exit}, nil},
		{"stopped by the session", []string{bash("b1"), `{"type":"user","toolUseResult":{"message":"Successfully stopped task: b1","task_id":"b1"}}`, exit}, nil},
		{"killed by the exit itself", []string{bash("b1"), exit, notice("b1", "killed")}, []string{"background Bash b1: run b1"}},
		{"cut off by an earlier switch", []string{bash("b1"), exit, turn, bash("b2"), exit}, []string{"background Bash b2: run b2"}},
		{"nothing since the earlier switch", []string{bash("b1"), exit, exit, turn, exit}, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "s.jsonl")
			write(t, path, strings.Join(c.lines, "\n")+"\n")
			if got := BackgroundTasks(path); !reflect.DeepEqual(got, c.want) {
				t.Fatalf("got %q, want %q", got, c.want)
			}
		})
	}
}

func TestBackgroundTasksNameWhatASubagentLeftRunning(t *testing.T) {
	shell := func(id string) string {
		return `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"use-` + id + `","name":"Bash","input":{"description":"Wait until the crew marker file exists"}}]}}` + "\n" +
			`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"use-` + id + `"}]},"toolUseResult":{"backgroundTaskId":"` + id + `"}}`
	}
	agent := `{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"use-a"}]},"toolUseResult":{"isAsync":true,"status":"async_launched","agentId":"a6ef","description":"crewwait"}}`
	notice := func(id, status string) string {
		return `{"type":"queue-operation","operation":"enqueue","content":"<task-notification>\n<task-id>` + id + `</task-id>\n<status>` + status + `</status>\n</task-notification>"}`
	}
	exit := `{"type":"cost-state","totalDuration":1,"startTime":1}`
	turn := `{"type":"user","message":{"content":"ym switched this conversation"}}`
	cutOff := "background Bash br43: Wait until the crew marker file exists"
	for _, c := range []struct {
		name      string
		main, sub []string
		want      []string
	}{
		{"the agent reported in, its shell died with the process", []string{agent, notice("a6ef", "completed"), exit, notice("br43", "killed")}, []string{shell("br43")}, []string{cutOff}},
		{"the agent still runs, and so does its shell", []string{agent, exit}, []string{shell("br43")}, []string{"background Agent a6ef: crewwait", cutOff}},
		{"its shell finished, seen in the subagent", []string{agent, notice("a6ef", "completed"), exit}, []string{shell("br43"), notice("br43", "completed")}, nil},
		{"its shell finished, seen in the main transcript", []string{agent, notice("a6ef", "completed"), notice("br43", "completed"), exit}, []string{shell("br43")}, nil},
		{"the agent belongs to an earlier run", []string{agent, notice("a6ef", "completed"), exit, turn, exit}, []string{shell("br43")}, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "s.jsonl")
			write(t, path, strings.Join(c.main, "\n")+"\n")
			sub := filepath.Join(strings.TrimSuffix(path, ".jsonl"), "subagents", "agent-a6ef.jsonl")
			if err := os.MkdirAll(filepath.Dir(sub), 0700); err != nil {
				t.Fatal(err)
			}
			write(t, sub, strings.Join(c.sub, "\n")+"\n")
			if got := BackgroundTasks(path); !reflect.DeepEqual(got, c.want) {
				t.Fatalf("got %q, want %q", got, c.want)
			}
		})
	}
}
