package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
)

// monitorWorkService ends serve when ym service restart asks, and reports an upgrade once the executable on disk differs from
// initial, the one this serve started from.
func monitorWorkService(ctx context.Context, cancel context.CancelFunc, dir string, initial os.FileInfo, upgraded chan<- bool) {
	self, _ := os.Executable()
	stop := filepath.Join(dir, "work-service-stop")
	marker, _ := os.ReadFile(stop)
	var previous os.FileInfo
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			currentMarker, _ := os.ReadFile(stop)
			if string(currentMarker) != string(marker) {
				upgraded <- false
				cancel()
				return
			}
			current, err := os.Stat(self)
			if err != nil || initial == nil {
				continue
			}
			if sameServiceBinary(initial, current) {
				previous = nil
				continue
			}
			if previous != nil && sameServiceBinary(previous, current) {
				upgraded <- true
				cancel()
				return
			}
			previous = current
		}
	}
}

func sameServiceBinary(a, b os.FileInfo) bool {
	return os.SameFile(a, b) && a.Size() == b.Size() && a.ModTime().Equal(b.ModTime())
}

func serviceCmd() *cobra.Command {
	root := &cobra.Command{Use: "service", Short: "Inspect or restart background reconciliation"}
	root.AddCommand(&cobra.Command{Use: "status", Short: "Show service heartbeat and its last error", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		dir, err := stateDir()
		if err != nil {
			return err
		}
		raw, err := os.ReadFile(filepath.Join(dir, "work-service.json"))
		if os.IsNotExist(err) {
			fmt.Fprintln(cmd.OutOrStdout(), "Background service has not started. Run ym or ym serve.")
			return nil
		}
		if err != nil {
			return err
		}
		var status map[string]any
		if err = json.Unmarshal(raw, &status); err != nil {
			return err
		}
		lock, err := os.OpenFile(filepath.Join(dir, "work-service.lock"), os.O_CREATE|os.O_RDWR, 0600)
		if err != nil {
			return err
		}
		defer lock.Close()
		status["running"] = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil
		if runtime.GOOS == "darwin" {
			if path, loaded, pid := launchdState(); loaded {
				status["kept"], status["kept_pid"] = path, pid
			}
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(status)
	}}, &cobra.Command{Use: "restart", Short: "Restart reconciliation with this shell's credentials and environment", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		dir, err := stateDir()
		if err != nil {
			return err
		}
		startLock, err := os.OpenFile(filepath.Join(dir, "work-start.lock"), os.O_CREATE|os.O_RDWR, 0600)
		if err != nil {
			return err
		}
		defer startLock.Close()
		if err = syscall.Flock(int(startLock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
			return fmt.Errorf("another service start or restart is active")
		}
		if err = os.WriteFile(filepath.Join(dir, "work-service-stop"), []byte(fmt.Sprint(time.Now().UnixNano())), 0600); err != nil {
			return err
		}
		lock, err := os.OpenFile(filepath.Join(dir, "work-service.lock"), os.O_CREATE|os.O_RDWR, 0600)
		if err != nil {
			return err
		}
		defer lock.Close()
		deadline := time.NewTimer(40 * time.Second)
		defer deadline.Stop()
		ticker := time.NewTicker(50 * time.Millisecond)
		defer ticker.Stop()
		for {
			if syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) == nil {
				lock.Close()
				if err = startWorkService(); err != nil {
					return err
				}
				fmt.Fprintln(cmd.OutOrStdout(), "Background service started with this environment; native agents continue independently.")
				return nil
			}
			select {
			case <-cmd.Context().Done():
				return cmd.Context().Err()
			case <-deadline.C:
				return fmt.Errorf("ym serve has not stopped; see %s", filepath.Join(dir, "work-service.log"))
			case <-ticker.C:
			}
		}
	}}, &cobra.Command{Use: "ensure", Short: "Start background reconciliation unless it already runs", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		return ensureWorkService()
	}}, &cobra.Command{Use: "keep", Short: "Keep background reconciliation running on this host across logouts and reboots, through a launchd agent on macOS or a systemd user timer", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		if runtime.GOOS == "darwin" {
			path, err := keepLaunchd()
			if err != nil {
				return fmt.Errorf("%w. Fix: %s", err, keepFix)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "ym serve is kept running by %s\n", path)
			return nil
		}
		dir, unit, err := keepWorkService()
		if err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "ym serve is kept running by %s/%s.timer\n", dir, unit)
		return nil
	}}, &cobra.Command{Use: "unkeep", Short: "Stop keeping background reconciliation running across reboots; the running ym serve stays up until it exits", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		if runtime.GOOS == "darwin" {
			path, err := unkeepLaunchd()
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "ym serve is no longer kept running; removed %s\n", path)
			return nil
		}
		dir, unit, err := unkeepWorkService()
		if err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "ym serve is no longer kept running; removed %s/%s.timer\n", dir, unit)
		return nil
	}}, &cobra.Command{Use: "run", Hidden: true, Short: "Run ym serve in the login shell's environment, as the launchd agent ym service keep writes does", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		return serviceRun(cmd.ErrOrStderr())
	}})
	return root
}

// unkeepWorkService stops and removes the systemd user units keepWorkService installed.
func unkeepWorkService() (string, string, error) {
	state, err := stateDir()
	if err != nil {
		return "", "", err
	}
	unit, err := serveUnit(state)
	if err != nil {
		return "", "", err
	}
	config, err := os.UserConfigDir()
	if err != nil {
		return "", "", err
	}
	dir := filepath.Join(config, "systemd", "user")
	bus := os.Environ()
	if os.Getenv("XDG_RUNTIME_DIR") == "" {
		bus = append(bus, fmt.Sprintf("XDG_RUNTIME_DIR=/run/user/%d", os.Getuid()))
	}
	c := exec.Command("systemctl", "--user", "disable", "--now", unit+".timer")
	c.Env = bus
	if out, err := c.CombinedOutput(); err != nil {
		return "", "", fmt.Errorf("systemctl --user disable --now %s.timer: %w: %s", unit, err, strings.TrimSpace(string(out)))
	}
	for _, name := range []string{unit + ".service", unit + ".timer"} {
		if err := os.Remove(filepath.Join(dir, name)); err != nil && !os.IsNotExist(err) {
			return "", "", err
		}
	}
	return dir, unit, nil
}

// keepWorkService installs and starts a systemd user timer that starts ym serve at boot and every minute it is not running, so a
// host's goals keep moving with no board open anywhere. An ssh command has no XDG_RUNTIME_DIR, which systemctl --user needs.
func keepWorkService() (string, string, error) {
	if _, err := exec.LookPath("systemctl"); err != nil {
		return "", "", fmt.Errorf("keeping ym serve running needs systemd: %w", err)
	}
	self, err := os.Executable()
	if err != nil {
		return "", "", err
	}
	state, err := stateDir()
	if err != nil {
		return "", "", err
	}
	unit, err := serveUnit(state)
	if err != nil {
		return "", "", err
	}
	config, err := os.UserConfigDir()
	if err != nil {
		return "", "", err
	}
	dir := filepath.Join(config, "systemd", "user")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", "", err
	}
	env := "Environment=" + strconv.Quote("PATH="+filepath.Dir(self)+":"+os.Getenv("PATH")) + "\n"
	for _, k := range []string{"YAMNAYA_STATE_DIR", "YAMNAYA_YM_SOCKET", "YAMNAYA_TMUX_SOCKET", "YAMNAYA_BRIEFS_ADDR"} {
		if v := os.Getenv(k); v != "" {
			env += "Environment=" + strconv.Quote(k+"="+v) + "\n"
		}
	}
	units := map[string]string{
		unit + ".service": "[Unit]\nDescription=Start ym serve unless it runs\n\n[Service]\nType=oneshot\nKillMode=process\n" + env +
			"ExecStart=" + self + " service ensure\n",
		unit + ".timer": "[Unit]\nDescription=Keep ym serve running\n\n[Timer]\nOnBootSec=30s\nOnUnitActiveSec=1min\n\n[Install]\nWantedBy=timers.target\n",
	}
	for name, text := range units {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644); err != nil {
			return "", "", err
		}
	}
	bus := os.Environ()
	if os.Getenv("XDG_RUNTIME_DIR") == "" {
		bus = append(bus, fmt.Sprintf("XDG_RUNTIME_DIR=/run/user/%d", os.Getuid()))
	}
	for _, args := range [][]string{{"--user", "daemon-reload"}, {"--user", "enable", "--now", unit + ".timer"}, {"--user", "start", unit + ".service"}} {
		c := exec.Command("systemctl", args...)
		c.Env = bus
		if out, err := c.CombinedOutput(); err != nil {
			return "", "", fmt.Errorf("systemctl %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
		}
	}
	return dir, unit, nil
}

// serveUnit names the systemd units that keep ym serve running for the state directory state: ym-serve for the default one, and a
// name of its own for any other, so keeping a second ym's serve never replaces the first's.
func serveUnit(state string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	if state, err = filepath.Abs(state); err != nil {
		return "", err
	}
	if state == filepath.Join(home, ".local", "state", "yamnaya") {
		return "ym-serve", nil
	}
	sum := sha256.Sum256([]byte(state))
	return "ym-serve-" + hex.EncodeToString(sum[:4]), nil
}
