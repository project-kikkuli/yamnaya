package repair

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"math/bits"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// Net are the network probes this machine answers itself: DNS, routes, VPN clients and TCP.
type Net struct {
	// Dial and Read bound a TCP connect and the wait for an ssh banner; a host's ssh front end can stall for half a minute.
	Dial, Read time.Duration
}

func NewNet() Net { return Net{Dial: 5 * time.Second, Read: 35 * time.Second} }

func (Net) Resolve(host string) ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return net.DefaultResolver.LookupHost(ctx, host)
}

// ResolvePublic asks 1.1.1.1 directly, past any VPN's resolver, so a name a VPN maps into its own range shows its real address.
func (Net) ResolvePublic(host string) ([]string, error) {
	r := &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, network, "1.1.1.1:53")
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return r.LookupHost(ctx, host)
}

func (Net) Route(ip string) (Route, error) {
	if runtime.GOOS == "linux" {
		out, err := exec.Command("ip", "route", "get", ip).Output()
		if err != nil {
			return Route{}, fmt.Errorf("ip route get: %w", err)
		}
		f := strings.Fields(string(out))
		var r Route
		for i := 0; i+1 < len(f); i++ {
			switch f[i] {
			case "dev":
				r.Iface = f[i+1]
			case "via":
				r.Gateway = f[i+1]
			}
		}
		return r, nil
	}
	out, err := exec.Command("route", "-n", "get", ip).Output()
	if err != nil {
		return Route{}, fmt.Errorf("route get: %w", err)
	}
	return parseRoute(string(out)), nil
}

// parseRoute reads macOS's route -n get: the interface, gateway, and the destination and mask of the route it matched.
func parseRoute(out string) Route {
	var r Route
	var dest, mask string
	for _, line := range strings.Split(out, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), ":")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		switch k {
		case "interface":
			r.Iface = v
		case "gateway":
			r.Gateway = v
		case "destination":
			dest = v
		case "mask":
			mask = v
		}
	}
	if dest != "" && dest != "default" {
		r.Net = dest
		if m := net.ParseIP(mask).To4(); m != nil {
			ones := bits.OnesCount32(uint32(m[0])<<24 | uint32(m[1])<<16 | uint32(m[2])<<8 | uint32(m[3]))
			r.Net = fmt.Sprintf("%s/%d", dest, ones)
		}
	}
	return r
}

func (Net) Tunnels() []Tunnel {
	var out []Tunnel
	if t, ok := twingate(); ok {
		out = append(out, t)
	}
	if t, ok := tailscale(); ok {
		out = append(out, t)
	}
	return out
}

// twingate reads Twingate's state from its macOS network service, else from its Linux CLI.
func twingate() (Tunnel, bool) {
	t := Tunnel{Kind: "Twingate"}
	if runtime.GOOS == "darwin" {
		out, _ := exec.Command("scutil", "--nc", "list").Output()
		found := false
		for _, line := range strings.Split(string(out), "\n") {
			if !strings.Contains(strings.ToLower(line), "twingate") {
				continue
			}
			found = true
			if strings.Contains(line, "(Connected)") {
				t.Connected = true
			}
		}
		t.Running = processRunning("Twingate")
		return t, found || t.Running
	}
	bin, err := exec.LookPath("twingate")
	if err != nil {
		return t, false
	}
	out, _ := exec.Command(bin, "status").CombinedOutput()
	state := strings.TrimSpace(string(out))
	t.Running = state != "" && !strings.Contains(state, "not-running")
	t.Connected = state == "online"
	t.Detail = "twingate status: " + state
	return t, true
}

func tailscale() (Tunnel, bool) {
	t := Tunnel{Kind: "Tailscale"}
	bin, err := exec.LookPath("tailscale")
	if err != nil {
		bin = "/Applications/Tailscale.app/Contents/MacOS/Tailscale"
		if _, err := os.Stat(bin); err != nil {
			return t, false
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "status", "--json").Output()
	if err != nil {
		return t, true
	}
	var st struct {
		BackendState string
		Peer         map[string]struct{ TailscaleIPs []string }
	}
	if json.Unmarshal(out, &st) != nil {
		return t, true
	}
	t.Running, t.Connected, t.Detail = st.BackendState != "", st.BackendState == "Running", st.BackendState
	for _, p := range st.Peer {
		t.Peers = append(t.Peers, p.TailscaleIPs...)
	}
	return t, true
}

func processRunning(name string) bool {
	out, _ := exec.Command("pgrep", "-x", name).Output()
	return len(strings.TrimSpace(string(out))) > 0
}

func (n Net) Connect(addr string) (time.Duration, error) {
	start := time.Now()
	c, err := net.DialTimeout("tcp", addr, n.Dial)
	took := time.Since(start)
	if err != nil {
		return took, err
	}
	c.Close()
	return took, nil
}

func (n Net) Banner(addr string) (string, time.Duration, error) {
	start := time.Now()
	c, err := net.DialTimeout("tcp", addr, n.Dial)
	if err != nil {
		return "", time.Since(start), err
	}
	defer c.Close()
	c.SetReadDeadline(time.Now().Add(n.Read))
	line, err := bufio.NewReader(c).ReadString('\n')
	took := time.Since(start)
	if err != nil {
		return "", took, err
	}
	return strings.TrimSpace(line), took, nil
}

func (Net) Now() time.Time { return time.Now() }
