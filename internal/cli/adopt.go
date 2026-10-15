package cli

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/charmbracelet/x/term"
	"github.com/spf13/cobra"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/launch"
)

// Adopt waits until s sits idle, ends its agent in its current terminal and resumes the conversation on ym's server.
func (l launcher) Adopt(s core.Live, say func(string)) (core.Target, error) {
	if !s.Adoptable() {
		return core.Target{}, fmt.Errorf("%s runs in %s; ym open reaches it there", core.Short(s.Key()), s.Location())
	}
	st := launch.Stopper{
		Look:   func() (core.Live, error) { return l.core.Find(s.ID) },
		Runs:   func(pid int) (bool, bool) { return l.core.Runs(pid, s.Harness, s.ID) },
		Signal: syscall.Kill,
		Poll:   250 * time.Millisecond,
		Grace:  3 * time.Second,
		Say:    say,
	}
	last, err := st.Stop(s)
	if err != nil {
		return core.Target{}, err
	}
	if last.Cwd == "" {
		return core.Target{}, fmt.Errorf("%s ended, but ym could not read its directory to resume it in", core.Short(s.ID))
	}
	rec := launch.Adopted(last, time.Now())
	if err := l.records.Save(rec); err != nil {
		return core.Target{}, err
	}
	if _, err := l.revive(rec); err != nil {
		return core.Target{}, err
	}
	pane, state := l.state(rec)
	if state != "running" {
		return core.Target{}, fmt.Errorf("%s %s right after resuming on ym's server", rec.Key, state)
	}
	if say != nil {
		say(fmt.Sprintf("adopted as %s", rec.Key))
	}
	return core.Target{Session: last.Session, Handle: &core.Handle{Host: l.srv.Name(), ID: pane.ID, Label: pane.Session}}, nil
}

// Send types text into s when it runs on ym's server, holding it while a permission prompt shows; any other session gets it from ym item inbox.
func (l launcher) Send(s core.Live, text string) (string, error) {
	if s.ID == "" {
		return "", fmt.Errorf("pid %d has no session id yet", s.PID)
	}
	var pane *serverPane
	if s.Handle != nil && s.Handle.Host == l.srv.Name() {
		pane = l.pane(s.Handle.ID, s.Harness)
	}
	return l.send(s.ID, pane, text)
}

func (l launcher) send(session string, pane *serverPane, text string) (string, error) {
	snd := launch.Sender{Box: l.core.Store}
	var p launch.Pane
	if pane != nil {
		p = pane
	}
	m, out, err := snd.Send(session, p, text)
	if err != nil {
		return "", err
	}
	if out == launch.Held {
		if err := detach("send-hold", strconv.FormatInt(m.Seq, 10), pane.id, pane.harness.Name()); err != nil {
			return "", err
		}
	}
	return fmt.Sprintf("sent to %s, %s", core.Short(session), out), nil
}

// serverPane is a pane on ym's server, read and typed into through tmux.
type serverPane struct {
	l       launcher
	id      string
	harness core.Harness
}

func (l launcher) pane(id, harnessName string) *serverPane {
	for _, h := range l.core.Harnesses {
		if h.Name() == harnessName {
			return &serverPane{l: l, id: id, harness: h}
		}
	}
	return nil
}

func (p *serverPane) Prompt() (bool, bool) {
	if !p.l.srv.Alive(p.id) {
		return false, false
	}
	screen, err := p.l.srv.Screen(core.Handle{ID: p.id})
	if err != nil {
		return false, false
	}
	_, shown := p.harness.ParsePrompt(screen)
	return shown, true
}

func (p *serverPane) Type(text string) error { return p.l.srv.Type(core.Handle{ID: p.id}, text) }

func adoptCmd(with func(runFunc) func(*cobra.Command, []string) error) *cobra.Command {
	return &cobra.Command{
		Use:   "adopt <session>",
		Short: "Move a session running in another terminal onto ym's server: once idle, end it there and resume it here",
		Args:  cobra.ExactArgs(1),
		RunE: with(func(e *env, cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			if e.core.Launches == nil {
				return errors.New("ym's launcher is unavailable")
			}
			s, err := e.core.Find(args[0])
			if err != nil {
				return err
			}
			if !s.Adoptable() {
				fmt.Fprintf(out, "%s runs in %s; ym open %s reaches it there\n", core.Short(s.ID), s.Location(), core.Short(s.ID))
				return nil
			}
			t, err := e.core.Launches.Adopt(s, func(msg string) { fmt.Fprintln(out, msg) })
			if err != nil {
				return err
			}
			c := e.core.AttachCmd(t)
			if c == nil || !term.IsTerminal(os.Stdin.Fd()) {
				fmt.Fprintf(out, "open: ym open %s\n", core.Short(s.ID))
				return nil
			}
			return e.attach(c)
		}),
	}
}

func sendCmd(with func(runFunc) func(*cobra.Command, []string) error) *cobra.Command {
	return &cobra.Command{
		Use:   "send <session> <message>",
		Short: "Send a session a follow-up: typed in on ym's server even mid-turn, otherwise read from ym item inbox on its next turn",
		Args:  cobra.MinimumNArgs(2),
		RunE: with(func(e *env, cmd *cobra.Command, args []string) error {
			l, ok := e.core.Launches.(launcher)
			if !ok {
				return errors.New("ym's launcher is unavailable")
			}
			text := strings.Join(args[1:], " ")
			var msg string
			if rec, found := l.records.Find(args[0]); found && rec.Session != "" {
				pane, state := l.state(rec)
				var p *serverPane
				if state == "running" {
					p = l.pane(pane.ID, rec.Harness)
				}
				msg, err := l.send(rec.Session, p, text)
				if err != nil {
					return err
				}
				fmt.Fprintln(cmd.OutOrStdout(), msg)
				return nil
			}
			s, err := e.core.Find(args[0])
			switch {
			case err == nil:
				msg, err = l.Send(s, text)
			case errors.Is(err, core.ErrNotFound) && len(args[0]) >= 32:
				msg, err = l.send(args[0], nil, text)
			}
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), msg)
			return nil
		}),
	}
}

func sendHoldCmd(with func(runFunc) func(*cobra.Command, []string) error) *cobra.Command {
	return &cobra.Command{
		Use:    "send-hold <seq> <pane> <harness>",
		Hidden: true,
		Args:   cobra.ExactArgs(3),
		RunE: with(func(e *env, cmd *cobra.Command, args []string) error {
			l, ok := e.core.Launches.(launcher)
			if !ok {
				return errors.New("ym's launcher is unavailable")
			}
			seq, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return err
			}
			p := l.pane(args[1], args[2])
			if p == nil {
				return fmt.Errorf("unknown harness %q", args[2])
			}
			_, err = launch.Sender{Box: l.core.Store, Poll: 500 * time.Millisecond}.Hold(seq, p)
			return err
		}),
	}
}
