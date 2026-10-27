package cli

import (
	"io"
	"os"
	"os/exec"
	"os/signal"
	"sync"
	"syscall"

	"github.com/charmbracelet/x/term"
	"github.com/spf13/cobra"
	"golang.org/x/sys/unix"
)

func attachRelayCmd() *cobra.Command {
	return &cobra.Command{
		Use:                "attach-relay <host> [ym args...]",
		Short:              "Run ym args on host in this terminal through the relay ym remote attaches with",
		Args:               cobra.MinimumNArgs(1),
		Hidden:             true,
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			r := &remoteHosts{ssh: sshBin()}
			c := exec.Command(r.ssh, append(sshArgs(args[0], "-t"), attachCommand(args[1:]))...)
			clicks := &rightClicks{}
			relay := r.relay(args[0])
			relay.clicked = clicks.take
			err := proxyTTY(c, os.Stdin, clicks.see, relay)
			if exit, ok := err.(*exec.ExitError); ok {
				os.Exit(exit.ExitCode())
			}
			return err
		},
	}
}

// proxyTTY runs c on a terminal of its own in place of in's, passing what in types through see to c, what c draws to out,
// and in's size to c's terminal. In's mode comes back when c ends, when this process is told to end, which c is told too,
// and when it panics.
func proxyTTY(c *exec.Cmd, in *os.File, see func([]byte), out io.Writer) error {
	master, slave, err := openPTY()
	if err != nil {
		return err
	}
	defer master.Close()
	ends := make(chan os.Signal, 3)
	signal.Notify(ends, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	defer func() { signal.Stop(ends); close(ends) }()
	restore := func() {}
	if state, err := term.MakeRaw(in.Fd()); err == nil {
		var once sync.Once
		restore = func() { once.Do(func() { term.Restore(in.Fd(), state) }) }
	}
	defer restore()
	resize := func() {
		if w, h, err := term.GetSize(in.Fd()); err == nil {
			unix.IoctlSetWinsize(int(master.Fd()), unix.TIOCSWINSZ, &unix.Winsize{Row: uint16(h), Col: uint16(w)})
		}
	}
	resize()
	winch := make(chan os.Signal, 1)
	signal.Notify(winch, syscall.SIGWINCH)
	defer func() { signal.Stop(winch); close(winch) }()
	go func() {
		for range winch {
			resize()
		}
	}()
	c.Stdin, c.Stdout, c.Stderr = slave, slave, slave
	c.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true}
	err = c.Start()
	slave.Close()
	if err != nil {
		return err
	}
	go func() {
		for sig := range ends {
			restore()
			c.Process.Signal(sig)
		}
	}()
	go func() {
		defer func() {
			if p := recover(); p != nil {
				restore()
				panic(p)
			}
		}()
		buf := make([]byte, 4096)
		for {
			n, err := in.Read(buf)
			if n > 0 {
				see(buf[:n])
				master.Write(buf[:n])
			}
			if err != nil {
				return
			}
		}
	}()
	io.Copy(out, master)
	return c.Wait()
}
