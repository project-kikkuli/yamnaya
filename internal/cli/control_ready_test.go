package cli

import (
	"bufio"
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestTheControlWaitsForTheNativeServerToListenOnTheSocketItBound(t *testing.T) {
	for _, c := range []struct {
		name    string
		listen  time.Duration
		exits   bool
		wantErr string
	}{
		{name: "listening a moment after it binds", listen: 200 * time.Millisecond},
		{name: "bound and never listening", listen: -1, wantErr: "did not become ready"},
		{name: "exiting before it listens", listen: -1, exits: true, wantErr: errControlExited.Error()},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir, err := os.MkdirTemp("/tmp", "ym-ready-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { os.RemoveAll(dir) })
			socket := filepath.Join(dir, "s")
			fd, err := syscall.Socket(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
			if err != nil {
				t.Fatal(err)
			}
			if err := syscall.Bind(fd, &syscall.SockaddrUnix{Name: socket}); err != nil {
				t.Fatal(err)
			}
			file := os.NewFile(uintptr(fd), socket)
			t.Cleanup(func() { file.Close() })
			done := make(chan struct{})
			if c.exits {
				time.AfterFunc(200*time.Millisecond, func() { close(done) })
			}
			if c.listen >= 0 {
				time.AfterFunc(c.listen, func() { serveInitialize(t, fd, file) })
			}
			client, err := awaitControl(context.Background(), socket, done, 2*time.Second)
			if c.wantErr == "" {
				if err != nil {
					t.Fatalf("connected with %v", err)
				}
				client.Close()
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.wantErr) || errors.Is(err, syscall.ECONNREFUSED) {
				t.Fatalf("error %v, want %q", err, c.wantErr)
			}
		})
	}
}

// serveInitialize listens on the bound socket and answers one client's initialize.
func serveInitialize(t *testing.T, fd int, file *os.File) {
	if err := syscall.Listen(fd, 1); err != nil {
		t.Error(err)
		return
	}
	l, err := net.FileListener(file)
	if err != nil {
		t.Error(err)
		return
	}
	go func() {
		defer l.Close()
		conn, err := l.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		r := bufio.NewReader(conn)
		if _, err := r.ReadString('\n'); err != nil {
			return
		}
		conn.Write([]byte(`{"id":1,"result":{}}` + "\n"))
		r.ReadString('\n')
	}()
}
