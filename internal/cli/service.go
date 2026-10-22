package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
)

func monitorWorkService(ctx context.Context, cancel context.CancelFunc, dir string, upgraded chan<- bool) {
	self, _ := os.Executable()
	initial, _ := os.Stat(self)
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
				return fmt.Errorf("service has not stopped; inspect work-service.log")
			case <-ticker.C:
			}
		}
	}}, &cobra.Command{Use: "ensure", Short: "Start background reconciliation unless it already runs", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		return ensureWorkService()
	}}, &cobra.Command{Use: "keep", Short: "Keep background reconciliation running on this host across logouts and reboots, through a systemd user timer", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		dir, err := keepWorkService()
		if err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "ym serve is kept running by %s/ym-serve.timer\n", dir)
		return nil
	}})
	return root
}

// keepWorkService installs and starts a systemd user timer that starts ym serve at boot and every minute it is not running, so a
// host's goals keep moving with no board open anywhere.
func keepWorkService() (string, error) {
	if _, err := exec.LookPath("systemctl"); err != nil {
		return "", fmt.Errorf("keeping ym serve running needs systemd: %w", err)
	}
	self, err := os.Executable()
	if err != nil {
		return "", err
	}
	config, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(config, "systemd", "user")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := "PATH=" + filepath.Dir(self) + ":" + os.Getenv("PATH")
	units := map[string]string{
		"ym-serve.service": "[Unit]\nDescription=Start ym serve unless it runs\n\n[Service]\nType=oneshot\nKillMode=process\nEnvironment=" +
			strconv.Quote(path) + "\nExecStart=" + self + " service ensure\n",
		"ym-serve.timer": "[Unit]\nDescription=Keep ym serve running\n\n[Timer]\nOnBootSec=30s\nOnUnitActiveSec=1min\n\n[Install]\nWantedBy=timers.target\n",
	}
	for name, text := range units {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644); err != nil {
			return "", err
		}
	}
	for _, args := range [][]string{{"--user", "daemon-reload"}, {"--user", "enable", "--now", "ym-serve.timer"}, {"--user", "start", "ym-serve.service"}} {
		if out, err := exec.Command("systemctl", args...).CombinedOutput(); err != nil {
			return "", fmt.Errorf("systemctl %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
		}
	}
	return dir, nil
}
