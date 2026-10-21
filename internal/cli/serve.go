package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/intake"
	"github.com/project-kikkuli/yamnaya/internal/proc"
	"github.com/project-kikkuli/yamnaya/internal/tui"
	"github.com/project-kikkuli/yamnaya/internal/work"
	"github.com/spf13/cobra"
)

func serveCmd(with func(runFunc) func(*cobra.Command, []string) error) *cobra.Command {
	var once bool
	var interval time.Duration
	var listen string
	var discover, requests bool
	cmd := &cobra.Command{Use: "serve", Short: "Reconcile work independently of the terminal and serve manager briefs", Args: cobra.NoArgs, RunE: with(func(e *env, cmd *cobra.Command, args []string) error {
		if interval <= 0 {
			return fmt.Errorf("interval must be positive")
		}
		if !discover {
			e.core.Harnesses = nil
			e.core.Procs = func() ([]proc.Proc, error) { return nil, nil }
			e.core.PRs = nil
		}
		dir, err := stateDir()
		if err != nil {
			return err
		}
		lock, err := os.OpenFile(filepath.Join(dir, "work-service.lock"), os.O_CREATE|os.O_RDWR, 0600)
		if err != nil {
			return err
		}
		defer lock.Close()
		if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
			return fmt.Errorf("work service already running: %w", err)
		}
		ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
		defer cancel()
		upgraded := make(chan bool, 1)
		if !once {
			go monitorWorkService(ctx, cancel, dir, upgraded)
		}
		var server *http.Server
		address := ""
		if !once {
			host, _, err := net.SplitHostPort(listen)
			if err != nil {
				return err
			}
			if host != "127.0.0.1" && host != "::1" && host != "localhost" {
				return fmt.Errorf("brief server must listen on loopback")
			}
			ln, err := net.Listen("tcp", listen)
			if err != nil {
				return err
			}
			address = "http://" + ln.Addr().String()
			server = &http.Server{Handler: work.BriefHandler(e.core.Store.Work), ReadHeaderTimeout: 5 * time.Second}
			defer server.Close()
			go func() {
				if err := server.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
					cancel()
				}
			}()
		}
		f := intake.New()
		f.Discover = requests
		for {
			err := workCycle(ctx, e.core, f)
			message := ""
			if err != nil {
				message = err.Error()
			}
			raw, _ := json.Marshal(map[string]any{"pid": os.Getpid(), "address": address, "at": time.Now().UTC(), "error": message})
			if writeErr := os.WriteFile(filepath.Join(dir, "work-service.json.new"), raw, 0600); writeErr != nil {
				return writeErr
			}
			if writeErr := os.Rename(filepath.Join(dir, "work-service.json.new"), filepath.Join(dir, "work-service.json")); writeErr != nil {
				return writeErr
			}
			if once {
				return err
			}
			if err != nil {
				fmt.Fprintln(cmd.ErrOrStderr(), err)
			}
			timer := time.NewTimer(interval)
			select {
			case <-ctx.Done():
				timer.Stop()
				select {
				case changed := <-upgraded:
					if changed {
						return tui.ErrRestart
					}
				default:
				}
				return nil
			case <-timer.C:
			}
		}
	})}
	cmd.Flags().BoolVar(&once, "once", false, "reconcile once without opening a terminal or HTTP listener")
	cmd.Flags().BoolVar(&discover, "discover", true, "discover native sessions on this host")
	cmd.Flags().BoolVar(&requests, "github-requests", true, "capture GitHub review requests addressed directly to your account")
	cmd.Flags().DurationVar(&interval, "interval", 5*time.Second, "background reconciliation cadence")
	cmd.Flags().StringVar(&listen, "listen", "127.0.0.1:7763", "loopback address for stable manager briefs")
	return cmd
}

func workCycle(ctx context.Context, c *core.Core, f intake.Fetcher) error {
	if err := c.Store.Work.RetireDiscoveredManagers(); err != nil {
		return err
	}
	if err := c.Store.Work.MigrateIntake(); err != nil {
		return err
	}
	if err := routeUnassignedIntake(c); err != nil {
		return err
	}
	b, err := c.Board()
	if err != nil {
		return err
	}
	if err = c.WorkCycle(b); err != nil {
		return err
	}
	if err = captureSessionPRs(c, b); err != nil {
		return err
	}
	var requestErr error
	if f.Discover {
		requestErr = captureRequests(ctx, c, f)
	}
	if _, err = c.Redeliver(b); err != nil {
		return err
	}
	var failures []error
	if _, err = c.RefreshPRs(b); err != nil {
		failures = append(failures, err)
	}
	if err = captureMergeReadiness(c); err != nil {
		return err
	}
	all, err := c.Store.Work.List()
	if err != nil {
		return err
	}
	budget := 16
	if requestErr != nil {
		failures = append(failures, requestErr)
	}
	type poll struct {
		scope    string
		previous string
		source   work.Source
		review   work.Review
		err      error
	}
	var polls []poll
	for _, w := range all {
		for _, source := range w.Sources {
			if r, ok := w.Reviews[source.URL]; ok && r.Closed {
				continue
			}
			if len(polls) == budget {
				break
			}
			if c.Store.Work.Due(w.ID, source.URL, time.Now()) {
				polls = append(polls, poll{scope: w.ID, source: source, previous: w.Reviews[source.URL].Version})
			}
		}
	}
	jobs := make(chan int)
	var workers sync.WaitGroup
	for i := 0; i < min(4, len(polls)); i++ {
		workers.Go(func() {
			for i := range jobs {
				polls[i].review, polls[i].err = f.Fetch(ctx, polls[i].source)
			}
		})
	}
	for i := range polls {
		jobs <- i
	}
	close(jobs)
	workers.Wait()
	for _, p := range polls {
		fetchErr := p.err
		if fetchErr == nil {
			current, getErr := c.Store.Work.Get(p.scope)
			fetchErr = getErr
			if getErr == nil && current.Reviews[p.review.ID].Version != p.review.Version {
				_, fetchErr = c.Store.Work.Commit(work.Event{ID: fmt.Sprintf("source/%s/%s/%s", p.scope, p.review.ID, p.review.Version), Work: p.scope, Expected: current.Revision, Type: "observe", Actor: "source", Review: p.review, PreviousVersion: p.previous})
			}
		}
		if saveErr := c.Store.Work.SourceError(p.scope, p.source.URL, fetchErr); saveErr != nil {
			failures = append(failures, saveErr)
		}
		if fetchErr != nil {
			failures = append(failures, fmt.Errorf("%s: %w", p.scope, fetchErr))
		}
	}
	if err = c.DeliverWork(b); err != nil {
		failures = append(failures, err)
	}
	peers, err := c.Store.Work.Peers()
	if err != nil {
		return err
	}
	for _, peer := range peers {
		commands, err := c.Store.Work.RemoteCommands(peer.Host)
		if err != nil {
			return err
		}
		for _, event := range commands {
			raw, _ := json.Marshal(event)
			peerContext, cancel := context.WithTimeout(ctx, 30*time.Second)
			run := exec.CommandContext(peerContext, "ssh", "-oBatchMode=yes", "-oConnectTimeout=5", peer.Host, remoteBin+" work apply")
			run.Stdin = bytes.NewReader(raw)
			out, applyErr := run.CombinedOutput()
			transportFailure := peerContext.Err() != nil
			cancel()
			var exit *exec.ExitError
			if errors.As(applyErr, &exit) && exit.ExitCode() == 255 {
				transportFailure = true
			}
			if transportFailure {
				continue
			}
			if applyErr != nil {
				applyErr = fmt.Errorf("remote judgment not applied: %s", bytes.TrimSpace(out))
			}
			if err = c.Store.Work.RemoteResult(event.ID, applyErr); err != nil {
				return err
			}
		}
		peerContext, cancel := context.WithTimeout(ctx, 30*time.Second)
		run := exec.CommandContext(peerContext, "ssh", "-oBatchMode=yes", "-oConnectTimeout=5", peer.Host, remoteBin+" work export")
		raw, fetchErr := run.Output()
		cancel()
		if fetchErr == nil {
			fetchErr = json.Unmarshal(raw, &peer.Scopes)
		}
		peer.Error = ""
		peer.Fetched = time.Now().UTC()
		if fetchErr != nil {
			peer.Error = fetchErr.Error()
		}
		if err = c.Store.Work.SavePeer(peer); err != nil {
			return err
		}
	}
	if err = reconcileHandoffs(c); err != nil {
		failures = append(failures, err)
	}
	if err = reconcileAgents(c, b); err != nil {
		failures = append(failures, err)
	}
	return errors.Join(failures...)
}

func ensureWorkService() error {
	dir, err := stateDir()
	if err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(dir, "work-start.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
		return nil
	}
	return startWorkService()
}

func startWorkService() error {
	dir, err := stateDir()
	if err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(dir, "work-service.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		lock.Close()
		return nil
	}
	lock.Close()
	self, err := os.Executable()
	if err != nil {
		return err
	}
	log, err := os.OpenFile(filepath.Join(dir, "work-service.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer log.Close()
	p := exec.Command(self, "serve")
	p.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	p.Stdout, p.Stderr = log, log
	if err = p.Start(); err != nil {
		return err
	}
	return p.Process.Release()
}
