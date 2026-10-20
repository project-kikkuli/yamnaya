package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/work"
	"github.com/spf13/cobra"
)

func wakeWork(c *core.Core, session, text string) error {
	all, err := c.Store.Work.List()
	if err != nil {
		return err
	}
	var execution work.Execution
	found := false
	for _, w := range all {
		if x, ok := w.Executions[session]; ok {
			execution, found = x, true
			break
		}
	}
	if !found || execution.Directory == "" {
		return fmt.Errorf("manager %s has no resumable execution metadata", session)
	}
	dir, err := stateDir()
	if err != nil {
		return err
	}
	sum := sha256.Sum256([]byte(session))
	name := hex.EncodeToString(sum[:12])
	lock, err := os.OpenFile(filepath.Join(dir, "work-run-"+name+".lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return fmt.Errorf("manager is already handling pending work")
	}
	var slot *os.File
	for i := 0; i < 4; i++ {
		candidate, e := os.OpenFile(filepath.Join(dir, fmt.Sprintf("work-slot-%d.lock", i)), os.O_CREATE|os.O_RDWR, 0600)
		if e != nil {
			return e
		}
		if syscall.Flock(int(candidate.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) == nil {
			slot = candidate
			break
		}
		candidate.Close()
	}
	if slot == nil {
		return fmt.Errorf("background manager capacity is occupied")
	}
	defer slot.Close()
	self, err := os.Executable()
	if err != nil {
		return err
	}
	log, err := os.OpenFile(filepath.Join(dir, "work-run-"+name+".log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer log.Close()
	cmd := exec.Command(self, "work-run", session)
	cmd.Env = append(os.Environ(), "YAMNAYA_WORK_SUPERVISED=1")
	cmd.ExtraFiles = []*os.File{lock, slot}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Stdout, cmd.Stderr = log, log
	if err = cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

func workRunCmd(with func(runFunc) func(*cobra.Command, []string) error) *cobra.Command {
	return &cobra.Command{Use: "work-run <session>", Hidden: true, Args: cobra.ExactArgs(1), RunE: with(func(e *env, cmd *cobra.Command, args []string) error {
		if os.Getenv("YAMNAYA_WORK_SUPERVISED") != "1" {
			return fmt.Errorf("work-run requires the background supervisor")
		}
		lock, slot := os.NewFile(3, "manager lock"), os.NewFile(4, "manager capacity")
		if lock == nil || slot == nil {
			return fmt.Errorf("work-run is invoked by the background service")
		}
		defer lock.Close()
		defer slot.Close()
		all, err := e.core.Store.Work.List()
		if err != nil {
			return err
		}
		var execution work.Execution
		for _, w := range all {
			if x, ok := w.Executions[args[0]]; ok {
				execution = x
				break
			}
		}
		prompt := "Read pending work with ym --session " + args[0] + " work inbox. Read the referenced scopes, current context and delegated authority. Incorporate changed direction and record the context revision with ym work adopt. Continue independently within that authority. Publish a current assessment with ym work brief using its current Context revision, evidence source versions, rationale, changed conclusions, uncertainty, next action and artifact address when available. Prepare reserved owner judgments with ym work prepare; never record the owner's judgment yourself. If more work needs an external event, persist its next trigger."
		argv := append([]string(nil), execution.Command...)
		if len(argv) == 0 {
			switch execution.Harness {
			case "claude":
				argv = []string{"claude", "--resume", args[0], "--print", prompt}
			case "codex":
				argv = []string{"codex", "exec", "resume", args[0], prompt}
			default:
				return fmt.Errorf("no native wake adapter for %s", execution.Harness)
			}
		} else {
			for i, a := range argv {
				argv[i] = strings.ReplaceAll(a, "{prompt}", prompt)
			}
		}
		run := exec.CommandContext(cmd.Context(), argv[0], argv[1:]...)
		run.Dir = execution.Directory
		run.Stdout, run.Stderr = cmd.OutOrStdout(), cmd.ErrOrStderr()
		for _, entry := range os.Environ() {
			if !strings.HasPrefix(entry, "CLAUDECODE=") {
				run.Env = append(run.Env, entry)
			}
		}
		e.core.Store.Work.RunResult(args[0], nil)
		err = run.Run()
		if saveErr := e.core.Store.Work.RunResult(args[0], err); saveErr != nil {
			return saveErr
		}
		return err
	})}
}
