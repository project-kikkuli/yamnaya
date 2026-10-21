package cli

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/google/uuid"
	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/work"
	"github.com/spf13/cobra"
)

func agentLock(dir, id string) (*os.File, error) {
	f, err := os.OpenFile(filepath.Join(dir, "agent-"+id+".lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

func reconcileAgents(c *core.Core, b core.Board) error {
	if err := c.Store.Work.DispatchAgentBatches(); err != nil {
		return err
	}
	agents, err := c.Store.Work.DueAgents()
	if err != nil {
		return err
	}
	dir, err := stateDir()
	if err != nil {
		return err
	}
	capacity, err := c.Store.Work.AgentCapacity()
	if err != nil {
		return err
	}
	for _, a := range agents {
		if a.Session != "" {
			if session, live := b.Session(a.Session); live {
				x, err := c.Store.Work.AgentSnapshot(a.ID)
				if err != nil {
					return err
				}
				raw, _ := json.Marshal(x.Pending)
				signature := fmt.Sprintf("%x", sha256.Sum256(raw))
				if !c.Store.Work.AgentNotified(a.ID, signature) && c.BackgroundSend != nil {
					if err = c.BackgroundSend(session, agentPrompt(x)); err == nil {
						if err = c.Store.Work.AgentMessagesDelivered(x.Messages); err != nil {
							return err
						}
						if err = c.Store.Work.AgentNotification(a.ID, signature); err != nil {
							return err
						}
					} else if err = c.Store.Work.AgentFailure(a.ID, x.Pending, err); err != nil {
						return err
					}
				}
				continue
			}
		}
		lock, err := agentLock(dir, a.ID)
		if errors.Is(err, syscall.EWOULDBLOCK) {
			continue
		}
		if err != nil {
			return err
		}
		slot, err := agentSlot(dir, capacity)
		if err != nil {
			lock.Close()
			return err
		}
		if slot == nil {
			lock.Close()
			break
		}
		self, err := os.Executable()
		if err != nil {
			slot.Close()
			lock.Close()
			return err
		}
		log, err := os.OpenFile(filepath.Join(dir, "agent-"+a.ID+".log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			slot.Close()
			lock.Close()
			return err
		}
		run := exec.Command(self, "agent-run", a.ID)
		run.Env = append(os.Environ(), "YAMNAYA_AGENT_SUPERVISED=1")
		run.ExtraFiles = []*os.File{lock, slot}
		run.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		run.Stdout, run.Stderr = log, log
		err = run.Start()
		slot.Close()
		lock.Close()
		log.Close()
		if err != nil {
			return err
		}
		if err = run.Process.Release(); err != nil {
			return err
		}
	}
	return nil
}

func agentSlot(dir string, capacity int) (*os.File, error) {
	for i := 0; i < capacity; i++ {
		f, err := os.OpenFile(filepath.Join(dir, fmt.Sprintf("work-slot-%d.lock", i)), os.O_CREATE|os.O_RDWR, 0600)
		if err != nil {
			return nil, err
		}
		if syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) == nil {
			return f, nil
		}
		f.Close()
	}
	return nil, nil
}

func agentRunCmd(with func(runFunc) func(*cobra.Command, []string) error) *cobra.Command {
	return &cobra.Command{Use: "agent-run <agent>", Hidden: true, Args: cobra.ExactArgs(1), RunE: with(func(e *env, cmd *cobra.Command, args []string) error {
		if os.Getenv("YAMNAYA_AGENT_SUPERVISED") != "1" {
			return errors.New("agent-run requires the background supervisor")
		}
		lock, slot := os.NewFile(3, "agent lock"), os.NewFile(4, "agent capacity")
		defer lock.Close()
		defer slot.Close()
		x, err := e.core.Store.Work.AgentSnapshot(args[0])
		if err != nil {
			return err
		}
		if len(x.Pending) == 0 || x.Agent.Paused {
			return nil
		}
		x.Agent.Running = true
		if err = e.core.Store.Work.SaveAgent(x.Agent); err != nil {
			return err
		}
		defer func() {
			if current, getErr := e.core.Store.Work.Agent(x.Agent.ID); getErr == nil {
				current.Running = false
				e.core.Store.Work.SaveAgent(current)
			}
		}()
		token := uuid.NewString()
		dir, err := stateDir()
		if err != nil {
			return err
		}
		report := filepath.Join(dir, "agent-"+x.Agent.ID+"-"+token+".json")
		defer os.Remove(report)
		schema := report + ".schema"
		if err = os.WriteFile(schema, checkpointSchema(x), 0600); err != nil {
			return err
		}
		defer os.Remove(schema)
		prompt := agentPrompt(x)
		self, selfErr := os.Executable()
		if selfErr != nil {
			return selfErr
		}
		prefix := "YAMNAYA_STATE_DIR=" + shellWord(dir) + " YAMNAYA_AGENT_ID=" + shellWord(x.Agent.ID) + " YAMNAYA_AGENT_RUN=" + shellWord(token) + " " + shellWord(self)
		prompt = "For EVERY ym command below, replace the word ym with this exact command prefix. Native tool shells may reset PATH and inherited environment; the prefix preserves your identity, run, state, and the matching CLI version:\n" + prefix + "\n\n" + prompt
		argv := append([]string(nil), x.Agent.Execution.Command...)
		promptPassed := len(argv) == 0
		if len(argv) == 0 {
			switch x.Agent.Execution.Harness {
			case "codex":
				if x.Agent.Session != "" {
					argv = []string{"codex", "exec", "resume", x.Agent.Session, "--yolo", "--json", "--output-schema", schema, "--output-last-message", report, prompt}
				} else {
					argv = []string{"codex", "exec", "--yolo", "--json", "--output-schema", schema, "--output-last-message", report, prompt}
				}
			case "claude":
				argv = []string{"claude", "--dangerously-skip-permissions", "--print", prompt}
				if x.Agent.Session != "" {
					argv = append(argv, "--resume", x.Agent.Session)
				} else {
					x.Agent.Session = uuid.NewString()
					argv = append(argv, "--session-id", x.Agent.Session)
					if err = e.core.Store.Work.SaveAgent(x.Agent); err != nil {
						return err
					}
				}
			default:
				return fmt.Errorf("unsupported native harness %s", x.Agent.Execution.Harness)
			}
		} else {
			for i, a := range argv {
				promptPassed = promptPassed || strings.Contains(a, "{prompt}")
				argv[i] = strings.ReplaceAll(a, "{prompt}", prompt)
			}
		}
		run := exec.CommandContext(cmd.Context(), argv[0], argv[1:]...)
		run.Dir = x.Agent.Execution.Directory
		run.ExtraFiles = []*os.File{lock, slot}
		for _, entry := range os.Environ() {
			if !strings.HasPrefix(entry, "CLAUDECODE=") && !strings.HasPrefix(entry, "YAMNAYA_AGENT_") {
				run.Env = append(run.Env, entry)
			}
		}
		run.Env = append(run.Env, "YAMNAYA_AGENT_ID="+x.Agent.ID, "YAMNAYA_AGENT_RUN="+token, "YAMNAYA_AGENT_REPORT="+report)
		output := agentOutput{Log: cmd.OutOrStdout()}
		if x.Agent.Execution.Harness == "codex" {
			output.Thread = func(thread string) error {
				current, err := e.core.Store.Work.Agent(x.Agent.ID)
				if err != nil {
					return err
				}
				if current.Session != "" {
					return nil
				}
				current.Session = thread
				return e.core.Store.Work.SaveAgent(current)
			}
		}
		run.Stdout = &output
		run.Stderr = cmd.ErrOrStderr()
		err = run.Start()
		if err == nil {
			var deliveryErr error
			if promptPassed {
				deliveryErr = e.core.Store.Work.AgentMessagesDelivered(x.Messages)
			}
			err = run.Wait()
			if deliveryErr != nil {
				return deliveryErr
			}
		}
		if output.Err != nil {
			return output.Err
		}

		if checkpointed, _ := e.core.Store.Work.HasCheckpoint(x.Agent.ID, token); checkpointed {
			return err
		}
		if err == nil {
			raw, readErr := os.ReadFile(report)
			if readErr != nil {
				raw = output.tail
			}
			c, parseErr := parseCheckpoint(raw)
			if parseErr == nil {
				err = e.core.Store.Work.Checkpoint(x.Agent.ID, token, c)
			} else {
				err = fmt.Errorf("native execution exited without a management checkpoint: %w", parseErr)
			}
		}
		if err != nil && !errors.Is(err, work.ErrConflict) {
			if saveErr := e.core.Store.Work.AgentFailure(x.Agent.ID, x.Pending, fmt.Errorf("%w; execution log: %s", err, filepath.Join(dir, "agent-"+x.Agent.ID+".log"))); saveErr != nil {
				return saveErr
			}
		}

		return err
	})}
}

func parseCheckpoint(raw []byte) (work.Checkpoint, error) {
	var c work.Checkpoint
	text := strings.TrimSpace(string(raw))
	text = strings.TrimPrefix(text, "```json")
	text = strings.TrimPrefix(text, "```")
	text = strings.TrimSuffix(strings.TrimSpace(text), "```")
	err := json.Unmarshal([]byte(text), &c)
	return c, err
}

func agentPrompt(x work.AgentSnapshot) string {
	filtered := x.Work[:0]
	for _, w := range x.Work {
		if _, pending := x.Pending[w.ID]; pending {
			filtered = append(filtered, w)
		}
	}
	x.Work = filtered
	raw, _ := json.Marshal(x)
	return `You are a persistent delegated agent managed by Yamnaya. Your identity, remit, authority, current brief, and pending input revisions follow below. Sessions are replaceable; this durable state is your working memory.
Act within the delegated authority. Investigate consequential changes. Advance owned commitments, delegate bounded outcomes to managers, inspect child evidence, and verify results rather than equating process exit with completion. Several chiefs may follow a project; a work commitment has exactly one accountable owner. Explicit owner direction is standing policy; label your inferences as inferences. Never silently promote inferred priorities to owner policy. Coordinate thematic issues across attached work and own their follow-through. Use the native tools and independent execution environment for actual engineering work.
Use ym agent snapshot ID and ym work show ID --json or history to drill down. Use ym agent spawn NAME --role manager --remit TEXT --authority TEXT --directory DIR --parent WORK to create a responsible child agent. Use ym work delegate PARENT NAME --agent AGENT --purpose TEXT --authority TEXT --next TEXT for a child commitment. Use ym agent attach ID WORK to follow existing work and ym agent replace ID to replace a failed native execution after investigating its cause. Use ym work result WORK EVIDENCE when work is ready for the delegator to verify, and ym work verify WORK EVIDENCE after independent verification of a child result. Publish source-bound owner judgments using ym work prepare; never record the owner's decision yourself. Owner feedback and decisions require follow-through, not another notification. After carrying out a decision, record evidence with ym work follow-through WORK REVIEW EVIDENCE. If it needs further independent work, continue or delegate; if it waits for an external condition, schedule WakeAt.
Keep your brief readable: current conclusion, reasons, what changed, uncertainty, and next action. Read the relevant history for messages and previous feedback. Update project context only when the remit permits it and preserve the provenance of direction. If waiting, name a concrete event, source, child, or owner decision. For external conditions needing polling include WakeAt in RFC3339. If more independent work remains, use Next and leave WaitFor empty so the service schedules another management pass. Do not finish a turn with an unowned promise.
End with ONLY a JSON management checkpoint (no prose outside it), or call ym agent checkpoint ID with that JSON on stdin. Shape: {"Brief":{"Assessment":"conclusion","Rationale":"reasons","Changed":"changes","Uncertainty":"unknowns","Context":CURRENT_WORKSPACE_CONTEXT,"Evidence":{}},"Inputs":{"WORK_ID":EXACT_PENDING_REVISION},"Next":"concrete next action","WaitFor":"concrete event, or empty if work can continue","WakeAt":"optional RFC3339"}. Only acknowledge input revisions you incorporated. Run ym agent snapshot again if your own actions or incoming direction changed the pending revisions. Put test results and other narrative evidence in Brief.Rationale. Brief.Evidence, if supplied through the checkpoint command, is reserved for registered review IDs mapped to their exact observed source versions; it is not a map of arbitrary test logs. Omit WakeAt or use null unless it is a real RFC3339 timestamp. A checkpoint does not complete any delegated outcome. A failed execution is surfaced to your delegator for investigation.
` + string(raw)
}

func shellWord(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
