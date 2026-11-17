package cli

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/launch"
)

func TestNativeResumeKeepsExplicitInputAndExcludesSyntheticHandoff(t *testing.T) {
	bins := t.TempDir()
	for _, h := range []string{"claude", "codex"} {
		if err := os.WriteFile(filepath.Join(bins, h), []byte("#!/bin/sh\n"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bins)
	for _, h := range []string{"claude", "codex"} {
		for _, prompt := range []string{"", "owner explicitly asked this"} {
			t.Run(h+prompt, func(t *testing.T) {
				rec := launch.Record{Harness: h, Session: "native", Handoff: "Synthetic profile recovery", Worktree: launch.Worktree{Path: t.TempDir()}}
				args, err := resumeArgv(rec, prompt)
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(strings.Join(args, " "), rec.Handoff) {
					t.Fatalf("synthetic recovery became native input: %q", args)
				}
				if prompt != "" && !slices.Contains(args, prompt) {
					t.Fatalf("explicit input lost: %q", args)
				}
			})
		}
	}
}

func TestRecoveryDirectiveIsDurableAndDeduplicatedPerRestart(t *testing.T) {
	store, err := core.OpenStore(filepath.Join(t.TempDir(), "ym.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	l := launcher{core: &core.Core{Store: store}}
	rec := launch.Record{Key: "work", Session: "native", Handoff: "Continue the interrupted assignment", AttachedAt: time.Date(2026, 11, 17, 1, 0, 0, 0, time.UTC)}
	for i := 0; i < 2; i++ {
		if err := l.queueRecoveryDirective(rec); err != nil {
			t.Fatal(err)
		}
	}
	actions, err := store.Activations()
	if err != nil || len(actions) != 1 {
		t.Fatalf("duplicate recovery actions: %v %v", actions, err)
	}
	if !strings.Contains(actions[0].Payload, rec.Handoff) {
		t.Fatalf("recovery context lost: %+v", actions[0])
	}
	rec.AttachedAt = rec.AttachedAt.Add(time.Second)
	if err := l.queueRecoveryDirective(rec); err != nil {
		t.Fatal(err)
	}
	actions, err = store.Activations()
	if err != nil || len(actions) != 2 {
		t.Fatalf("distinct restart disappeared: %v %v", actions, err)
	}
}

func recoveryDirectivePayload(t *testing.T, l launcher) string {
	t.Helper()
	if l.core == nil {
		t.Fatal("recovery fixture has no action ledger")
	}
	actions, err := l.core.Store.Activations()
	if err != nil {
		t.Fatal(err)
	}
	var notes []string
	for _, action := range actions {
		if action.Kind == "directive" && strings.HasPrefix(action.Payload, "Yamnaya resumed launch ") {
			notes = append(notes, action.Payload)
		}
	}
	return strings.Join(notes, "\n")
}
