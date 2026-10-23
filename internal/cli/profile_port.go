package cli

import (
	"bufio"
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/launch"
	"github.com/project-kikkuli/yamnaya/internal/nativefile"
	"github.com/project-kikkuli/yamnaya/internal/profile"
)

//go:embed profile_cs.py
var profileCSScript string

type portRequest struct {
	ClaudeRoot string `json:"claude_root"`
	CodexRoot  string `json:"codex_root"`
	SQLitePath string `json:"sqlite_path"`
	Cache      string `json:"cache"`
	Harness    string `json:"harness"`
	Session    string `json:"session"`
	Cwd        string `json:"cwd"`
	Transcript string `json:"transcript"`
	To         string `json:"to"`
	TargetRoot string `json:"target_root"`
}

func (l launcher) prepareProfilePort(s core.Session, to, id string) (core.PortPlan, error) {
	dir := filepath.Dir(l.records.Dir)
	ps, err := profile.Load(dir)
	if err != nil {
		return core.PortPlan{}, err
	}
	from, err := profile.Find(ps, s.Harness, s.Profile)
	if err != nil {
		return core.PortPlan{}, err
	}
	if err = from.DefaultConstraint(); err != nil {
		return core.PortPlan{}, err
	}
	if s.ConfigRoot != "" && from.Root != physicalProfileRoot(s.ConfigRoot) {
		return core.PortPlan{}, fmt.Errorf("source profile root disagrees with live conversation")
	}
	target, err := l.selectProfile(to, id)
	if err != nil {
		return core.PortPlan{}, err
	}
	if err = target.ValidateSubscription(s.Cwd); err != nil {
		return core.PortPlan{}, err
	}
	cs := os.Getenv("YAMNAYA_CS_BIN")
	if cs == "" {
		cs, err = exec.LookPath("cs")
		if err != nil {
			return core.PortPlan{}, fmt.Errorf("cs is unavailable: %w", err)
		}
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		return core.PortPlan{}, err
	}
	code, err := os.ReadFile(cs)
	if err != nil {
		return core.PortPlan{}, err
	}
	work, err := os.MkdirTemp(dir, ".profile-port-")
	if err != nil {
		return core.PortPlan{}, err
	}
	close := func() { os.RemoveAll(work) }
	fail := func(err error) (core.PortPlan, error) { close(); return core.PortPlan{}, err }
	snapshot := filepath.Join(work, "cs.py")
	if err = profile.Write(snapshot, code); err != nil {
		return fail(err)
	}
	wrapper := filepath.Join(work, "adapter.py")
	if err = profile.Write(wrapper, []byte(profileCSScript)); err != nil {
		return fail(err)
	}
	rec := launch.Record{Harness: s.Harness, Profile: from.ID, ConfigRoot: from.Root, SQLiteRoot: from.SQLiteRoot, ConfigExplicit: from.Explicit, Session: s.ID}
	if saved, ok := l.records.Find(s.ID); ok && saved.ConfigRoot == from.Root {
		rec = saved
	}
	request := portRequest{Harness: s.Harness, Session: s.ID, Cwd: s.Cwd, To: to, TargetRoot: target.Root, Transcript: filepath.Join(work, "source.jsonl"), Cache: filepath.Join(dir, "ports", s.Harness, from.ID, to, target.ID, "cs")}
	claude, codex := from, target
	if s.Harness == "codex" {
		claude, codex = target, from
	}
	request.ClaudeRoot, request.CodexRoot = claude.Root, codex.Root
	sqlRoot := sqliteRoot(codex)
	request.SQLitePath = filepath.Join(sqlRoot, "state_5.sqlite")
	if err = materializePortSource(rec, request.Transcript); err != nil {
		return fail(err)
	}
	requestPath := filepath.Join(work, "request.json")
	raw, _ := json.Marshal(request)
	if err = profile.Write(requestPath, raw); err != nil {
		return fail(err)
	}
	run := func(mode string) (string, error) {
		child := exec.Command(python, "-I", wrapper, snapshot, mode, requestPath)
		child.Dir = s.Cwd
		child.Env = target.Environment(from.Environment(os.Environ()))
		var out, errors bytes.Buffer
		child.Stdout, child.Stderr = &out, &errors
		if err := child.Run(); err != nil {
			return "", fmt.Errorf("profile-aware cs %s failed: %w: %s", mode, err, strings.TrimSpace(errors.String()))
		}
		return strings.TrimSpace(out.String()), nil
	}
	if _, err = run("check"); err != nil {
		return fail(err)
	}
	if to == "codex" {
		if err = ensurePortSchema(target); err != nil {
			return fail(err)
		}
	}
	children, err := l.nativeLifecycle(rec, from, true)
	if err != nil {
		return fail(err)
	}
	plan := core.PortPlan{Target: core.Session{Harness: to, Profile: target.ID, ProfileLabel: target.Label, Account: target.Account, ConfigRoot: target.Root, Cwd: s.Cwd}, Close: close}
	plan.Write = func() (string, error) {
		alive := false
		if l.core != nil && s.PID > 0 {
			alive, _ = l.core.Runs(s.PID, s.Harness, s.ID)
		}
		if s.Harness == "codex" && !alive {
			if err = l.drainNative(children, s.PID); err != nil {
				return "", err
			}
			if err = l.awaitDrain(children); err != nil {
				return "", err
			}
		}
		release := func() {}
		if s.Harness == "codex" && !alive {
			release, err = writerLock(from.Root, s.ID)
			if err != nil {
				return "", err
			}
		}
		defer release()
		if err = materializePortSource(rec, request.Transcript); err != nil {
			return "", err
		}
		return run("port")
	}
	return plan, nil
}
func materializePortSource(rec launch.Record, dest string) error {
	head := transcript(rec)
	if head == "" {
		return fmt.Errorf("selected source transcript is unavailable; refusing a prompt-only port")
	}
	if rec.Harness == "claude" {
		data, err := nativefile.ReadFile(head)
		if err != nil {
			return err
		}
		return profile.Write(dest, data)
	}
	var out bytes.Buffer
	seen := map[string]bool{}
	var appendHistory func(string, int64) error
	appendHistory = func(path string, end int64) error {
		if seen[path] {
			return fmt.Errorf("cyclic native source history")
		}
		seen[path] = true
		meta, err := readRolloutMeta(path)
		if err != nil {
			return err
		}
		if meta.Payload.Base != nil {
			paths, err := artifactClosure(rec.ConfigRoot, path)
			if err != nil {
				return err
			}
			var base string
			for _, candidate := range paths {
				m, err := readRolloutMeta(candidate)
				if err != nil {
					return err
				}
				if rolloutID(candidate, m) == meta.Payload.Base.ID {
					base = candidate
					break
				}
			}
			if base == "" {
				return fmt.Errorf("missing native history base")
			}
			if err = appendHistory(base, meta.Payload.Base.End); err != nil {
				return err
			}
		}
		data, err := nativefile.ReadFile(path)
		if err != nil {
			return err
		}
		if end >= 0 {
			if end > int64(len(data)) || end == 0 || data[end-1] != '\n' {
				return fmt.Errorf("invalid native history cutoff")
			}
			data = data[:end]
		}
		out.Write(data)
		return nil
	}
	if err := appendHistory(head, -1); err != nil {
		return err
	}
	return profile.Write(dest, out.Bytes())
}
func ensurePortSchema(p profile.Profile) error {
	required := []string{"id", "rollout_path", "created_at", "updated_at", "source", "model_provider", "cwd", "title", "sandbox_policy", "approval_mode", "tokens_used", "has_user_event", "archived", "cli_version", "first_user_message", "memory_mode", "created_at_ms", "updated_at_ms", "thread_source", "preview", "recency_at", "recency_at_ms", "history_mode"}
	check := func() bool {
		db, err := nativeDB(sqliteRoot(p))
		if err != nil || db == nil {
			return false
		}
		defer db.Close()
		rows, err := db.Query(`SELECT name FROM pragma_table_info('threads')`)
		if err != nil {
			return false
		}
		defer rows.Close()
		columns := map[string]bool{}
		for rows.Next() {
			var name string
			if rows.Scan(&name) == nil {
				columns[name] = true
			}
		}
		for _, name := range required {
			if !columns[name] {
				return false
			}
		}
		return true
	}
	if check() {
		return nil
	}
	if err := initializeNativeSchema(p); err != nil {
		return err
	}
	if !check() {
		return fmt.Errorf("native Codex schema is incompatible with the inspected cs writer")
	}
	return nil
}
func initializeNativeSchema(p profile.Profile) error {
	binary, err := bin("codex")
	if err != nil {
		return err
	}
	if err = os.MkdirAll(p.Root, 0700); err != nil {
		return err
	}
	if err = os.MkdirAll(sqliteRoot(p), 0700); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, binary, "app-server", "--listen", "stdio://", "-c", "sqlite_home="+strconv.Quote(sqliteRoot(p)), "-c", `cli_auth_credentials_store="ephemeral"`, "-c", `check_for_update_on_startup=false`, "-c", `analytics.enabled=false`)
	child.Env = p.Environment(os.Environ())
	child.Stderr = io.Discard
	input, err := child.StdinPipe()
	if err != nil {
		return err
	}
	output, err := child.StdoutPipe()
	if err != nil {
		return err
	}
	if err = child.Start(); err != nil {
		return err
	}
	defer func() { input.Close(); cancel(); child.Wait() }()
	reader := bufio.NewReader(output)
	call := func(id int, method string, params any) error {
		if err := json.NewEncoder(input).Encode(map[string]any{"id": id, "method": method, "params": params}); err != nil {
			return err
		}
		for {
			line, err := reader.ReadBytes('\n')
			if err != nil {
				return err
			}
			var reply struct {
				ID    int             `json:"id"`
				Error json.RawMessage `json:"error"`
			}
			if json.Unmarshal(line, &reply) != nil || reply.ID != id {
				continue
			}
			if len(reply.Error) > 0 {
				return fmt.Errorf("native Codex refused metadata initialization")
			}
			return nil
		}
	}
	if err = call(1, "initialize", map[string]any{"clientInfo": map[string]string{"name": "ym_profile_schema", "version": "1"}}); err != nil {
		return err
	}
	if err = json.NewEncoder(input).Encode(map[string]string{"method": "initialized"}); err != nil {
		return err
	}
	return call(2, "thread/list", map[string]int{"limit": 1})
}
