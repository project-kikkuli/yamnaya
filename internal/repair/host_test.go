package repair

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
)

const vm = "prod-forge-ws-rajat-mehndiratta-rajatsmachine.exe.xyz"

var now = time.Date(2026, 10, 28, 17, 0, 0, 0, time.UTC)

// fakeHost answers the doctor's probes from tables; a missing entry is a working default.
type fakeHost struct {
	dns, public map[string][]string
	routes      map[string]Route
	tunnels     []Tunnel
	dial        map[string]error
	banner      error
	auth        error
	master      Master
	shared      []error
	remote      []Remote
	install     error
	did         []string
}

func timeout() error { return fmt.Errorf("dial tcp: %w", os.ErrDeadlineExceeded) }

func healthy() *fakeHost {
	return &fakeHost{
		dns:    map[string][]string{vm: {"161.210.93.249"}, "exe.dev": {"100.21.24.47"}, "exe.xyz": {"52.35.87.134"}},
		routes: map[string]Route{},
		dial:   map[string]error{},
		master: Master{Socket: true, Alive: true, Detail: "Master running (pid=1)"},
		remote: []Remote{{Installed: true, Version: "v1", ServeRunning: true, ServeAt: now.Add(-5 * time.Second)}},
	}
}

func (f *fakeHost) Resolve(h string) ([]string, error) {
	if ips, ok := f.dns[h]; ok {
		return ips, nil
	}
	return nil, errors.New("no such host")
}
func (f *fakeHost) ResolvePublic(h string) ([]string, error) {
	if ips, ok := f.public[h]; ok {
		return ips, nil
	}
	return nil, errors.New("no such host")
}
func (f *fakeHost) Route(ip string) (Route, error) {
	if r, ok := f.routes[ip]; ok {
		return r, nil
	}
	return Route{Iface: "en0"}, nil
}
func (f *fakeHost) Tunnels() []Tunnel { return f.tunnels }
func (f *fakeHost) Connect(addr string) (time.Duration, error) {
	return 5 * time.Millisecond, f.dial[addr]
}
func (f *fakeHost) Banner(string) (string, time.Duration, error) {
	return "SSH-2.0-Go", 10 * time.Millisecond, f.banner
}
func (f *fakeHost) Auth(string) error    { return f.auth }
func (f *fakeHost) Master(string) Master { return f.master }
func (f *fakeHost) DropMaster(string)    { f.did = append(f.did, "drop") }
func (f *fakeHost) ClearSocket(string) error {
	f.did = append(f.did, "clear")
	return nil
}
func (f *fakeHost) Shared(string) error {
	f.did = append(f.did, "shared")
	if len(f.shared) == 0 {
		return nil
	}
	err := f.shared[0]
	f.shared = f.shared[1:]
	return err
}
func (f *fakeHost) Remote(string) (Remote, error) {
	r := f.remote[0]
	if len(f.remote) > 1 {
		f.remote = f.remote[1:]
	}
	return r, nil
}
func (f *fakeHost) Version() string { return "v1" }
func (f *fakeHost) Install(string) (string, error) {
	f.did = append(f.did, "install")
	return "ym linux/amd64 copied", f.install
}
func (f *fakeHost) Now() time.Time { return now }

// twingateRoute is today's outage: the VM's name maps into Twingate's range and leaves through its tunnel.
func twingateRoute(f *fakeHost) {
	f.dns[vm], f.dns["exe.dev"], f.dns["exe.xyz"] = []string{"100.104.149.111"}, []string{"100.104.150.7"}, []string{"100.104.150.8"}
	for _, ip := range []string{"100.104.149.111", "100.104.150.7", "100.104.150.8"} {
		f.routes[ip] = Route{Iface: "utun4", Net: "100.104.0.0/16"}
	}
	f.tunnels = []Tunnel{{Kind: "Twingate", Running: true, Connected: true}}
}

func TestHostDoctorVerdicts(t *testing.T) {
	for _, c := range []struct {
		name    string
		set     func(*fakeHost)
		verdict string
		did     []string
	}{
		{"healthy", func(*fakeHost) {}, "@rajatsmachine is healthy: ssh, the shared connection, ym and its serve all answer", []string{"shared"}},
		{"twingate route black-holes the VM and the provider", func(f *fakeHost) {
			twingateRoute(f)
			f.dial["100.104.149.111:22"], f.dial["100.104.150.7:22"], f.dial["100.104.150.8:22"] = timeout(), timeout(), timeout()
		}, "Twingate route to 100.104.0.0/16 is down: open Twingate and reconnect (exe.dev's and exe.xyz's ssh gateways are also unreachable, so this isn't your VM)", nil},
		{"twingate down but the VM answers at its public address", func(f *fakeHost) {
			twingateRoute(f)
			f.public = map[string][]string{vm: {"161.210.93.249"}}
			f.dns["exe.dev"], f.dns["exe.xyz"] = []string{"100.21.24.47"}, []string{"52.35.87.134"}
			f.dial["100.104.149.111:22"] = timeout()
		}, "Twingate route to 100.104.0.0/16 is down, but @rajatsmachine answers directly at 161.210.93.249: reconnect Twingate, or disconnect it to reach the VM directly", nil},
		{"only the VM is down behind twingate", func(f *fakeHost) {
			twingateRoute(f)
			f.dial["100.104.149.111:22"] = timeout()
		}, "only this VM is unreachable through Twingate: exe.dev answers, so the VM or its Twingate resource is down; restart the VM", nil},
		{"twingate routes but is disconnected", func(f *fakeHost) {
			twingateRoute(f)
			f.tunnels[0].Connected = false
			f.dial["100.104.149.111:22"] = timeout()
		}, "@rajatsmachine routes through Twingate, which is not connected: open Twingate and connect", nil},
		{"tailscale owns the peer address", func(f *fakeHost) {
			twingateRoute(f)
			f.tunnels = append(f.tunnels, Tunnel{Kind: "Tailscale", Running: true, Connected: true, Peers: []string{"100.104.149.111"}})
			f.dial["100.104.149.111:22"], f.dial["100.104.150.7:22"], f.dial["100.104.150.8:22"] = timeout(), timeout(), timeout()
		}, "Tailscale route to 100.104.0.0/16 is down: open Tailscale and reconnect (exe.dev's and exe.xyz's ssh gateways are also unreachable, so this isn't your VM)", nil},
		{"direct: the whole provider is down", func(f *fakeHost) {
			f.dial["161.210.93.249:22"], f.dial["100.21.24.47:22"], f.dial["52.35.87.134:22"] = timeout(), timeout(), timeout()
		}, "exe.dev and exe.xyz are unreachable too, so the whole provider or your network is down, not just this VM: check your connection and the provider's status", nil},
		{"direct: just this VM", func(f *fakeHost) { f.dial["161.210.93.249:22"] = timeout() },
			"just this VM: @rajatsmachine does not answer on port 22 while exe.dev does; restart the VM", nil},
		{"sshd refuses", func(f *fakeHost) { f.dial["161.210.93.249:22"] = syscall.ECONNREFUSED },
			"@rajatsmachine refuses ssh on port 22: the VM is up but its sshd is not; restart the VM", nil},
		{"dns fails here but not publicly", func(f *fakeHost) {
			delete(f.dns, vm)
			f.public = map[string][]string{vm: {"161.210.93.249"}}
			f.tunnels = []Tunnel{{Kind: "Twingate", Running: true, Connected: true}}
		}, "@rajatsmachine does not resolve here but public DNS gives 161.210.93.249: this machine's DNS is broken, often a VPN's resolver; reconnect Twingate, or quit it to use your network's DNS", nil},
		{"dns fails everywhere", func(f *fakeHost) { delete(f.dns, vm) },
			"@rajatsmachine does not resolve anywhere: check the name (ym remote forget " + vm + " if it is gone)", nil},
		{"no banner", func(f *fakeHost) { f.banner = timeout() },
			"@rajatsmachine takes TCP on port 22 but sends no ssh banner (no answer (timed out)): its ssh front end is stuck; try again in a minute, then restart the VM", nil},
		{"key refused", func(f *fakeHost) { f.auth = errors.New("exit status 255: user@host: Permission denied (publickey).") },
			"@rajatsmachine answers but refuses your ssh key: run ssh-add -l to check your agent holds the key ~/.ssh/config names for it, then ssh " + vm + " by hand", nil},
		{"host key changed", func(f *fakeHost) { f.auth = errors.New("exit status 255: Host key verification failed.") },
			"@rajatsmachine's host key changed or is unknown: if the VM was rebuilt, run ssh-keygen -R " + vm + ", then ssh " + vm + " by hand once", nil},
		{"dead master is dropped and rebuilt", func(f *fakeHost) { f.shared = []error{errors.New("no answer in 40s")} },
			"@rajatsmachine works again: dropped a dead shared ssh connection", []string{"shared", "drop", "shared"}},
		{"stale socket is cleared", func(f *fakeHost) { f.master = Master{Socket: true, Detail: "Connection refused"} },
			"@rajatsmachine works again: cleared a stale ssh socket", []string{"clear", "shared"}},
		{"shared connection stays broken", func(f *fakeHost) {
			f.master, f.shared = Master{}, []error{errors.New("mux_client_hello_exchange: write: Broken pipe")}
		},
			fmt.Sprintf("ssh logs in to @rajatsmachine on its own, but ym's shared connection still fails after a rebuild (mux_client_hello_exchange: write: Broken pipe): run ym remote repair %s again; if it repeats, rm /tmp/ym-%d-* and retry", vm, os.Getuid()), []string{"shared"}},
		{"old ym is reinstalled", func(f *fakeHost) {
			f.remote = []Remote{{Installed: true, ServeRunning: true, ServeAt: now}, {Installed: true, Version: "v1", ServeRunning: true, ServeAt: now}}
		}, "@rajatsmachine works again: installed current ym on @rajatsmachine", []string{"shared", "install"}},
		{"missing ym is installed", func(f *fakeHost) {
			f.remote = []Remote{{}, {Installed: true, Version: "v1", ServeRunning: true, ServeAt: now}}
		}, "@rajatsmachine works again: installed current ym on @rajatsmachine", []string{"shared", "install"}},
		{"install fails", func(f *fakeHost) {
			f.remote, f.install = []Remote{{Installed: true, Version: "v0"}}, errors.New("go build: exit status 1")
		}, "ym on @rajatsmachine needs installing and ym remote install failed: go build: exit status 1; run ym remote install " + vm, []string{"shared", "install"}},
		{"stuck serve survives a reinstall", func(f *fakeHost) {
			f.remote = []Remote{{Installed: true, Version: "v1", ServeRunning: true, ServeAt: now.Add(-10 * time.Minute)}}
		}, "ym serve on @rajatsmachine runs but its last cycle was 10m0s ago: run ym remote " + vm + " service restart, then read ~/.local/state/yamnaya/work-service.log there", []string{"shared", "install"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := healthy()
			c.set(f)
			var steps []Step
			got := Host(vm, "@rajatsmachine", f, func(s Step) { steps = append(steps, s) })
			if got != c.verdict {
				t.Errorf("verdict\n got %q\nwant %q", got, c.verdict)
			}
			if !slices.Equal(f.did, c.did) {
				t.Errorf("did %v, want %v", f.did, c.did)
			}
			if len(steps) == 0 || steps[0].Name != "dns" {
				t.Errorf("steps start %v, want dns first", steps)
			}
		})
	}
}

func TestHostDoctorRunsChecksInOrder(t *testing.T) {
	var names []string
	Host(vm, "", healthy(), func(s Step) {
		if !slices.Contains(names, s.Name) {
			names = append(names, s.Name)
		}
	})
	want := []string{"dns", "route", "tunnel", "tcp :22", "banner", "auth", "master", "ym", "serve"}
	if !slices.Equal(names, want) {
		t.Fatalf("steps %v, want %v", names, want)
	}
}

func TestParseRoute(t *testing.T) {
	for _, c := range []struct {
		out  string
		want Route
	}{
		{"   route to: 100.104.149.111\ndestination: 100.104.0.0\n       mask: 255.255.0.0\n  interface: utun4\n", Route{Iface: "utun4", Net: "100.104.0.0/16"}},
		{"   route to: 161.210.93.249\ndestination: default\n       mask: default\n    gateway: 192.168.128.1\n  interface: en0\n", Route{Iface: "en0", Gateway: "192.168.128.1"}},
	} {
		if got := parseRoute(c.out); got != c.want {
			t.Errorf("parseRoute = %+v, want %+v", got, c.want)
		}
	}
}

func TestStepLine(t *testing.T) {
	got := Step{Name: "tcp :22", Status: Failed, Detail: "no answer (timed out)", Took: 5 * time.Second}.String()
	if !strings.Contains(got, "fail") || !strings.Contains(got, "tcp :22") || !strings.HasSuffix(got, "(5s)") {
		t.Fatalf("step line %q", got)
	}
}
