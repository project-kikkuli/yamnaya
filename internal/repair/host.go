package repair

import (
	"errors"
	"fmt"
	"net"
	"os"
	"slices"
	"strings"
	"syscall"
	"time"
)

// Route is how this machine sends packets to an address: the interface, the network the route covers and its gateway.
type Route struct{ Iface, Net, Gateway string }

// Tunnel is a VPN client on this machine and whether it carries traffic now; Peers are addresses it is known to route.
type Tunnel struct {
	Kind               string
	Running, Connected bool
	Detail             string
	Peers              []string
}

func (t Tunnel) String() string {
	state := "not running"
	switch {
	case t.Connected:
		state = "connected"
	case t.Running:
		state = "running, not connected"
	}
	if t.Detail != "" {
		state += " (" + t.Detail + ")"
	}
	return t.Kind + " " + state
}

// Master is how ym's shared ssh connection to a host stands: whether its socket exists and a master answers on it.
type Master struct {
	Socket, Alive bool
	Detail        string
}

// Remote is ym as a host runs it: its source version and whether its ym serve runs and cycles.
type Remote struct {
	Installed    bool
	Version      string
	ServeRunning bool
	ServeAt      time.Time
}

// HostProbes are what the host doctor looks at and fixes; each runs on this machine, ssh included.
type HostProbes interface {
	Resolve(host string) ([]string, error)
	ResolvePublic(host string) ([]string, error)
	Route(ip string) (Route, error)
	Tunnels() []Tunnel
	Connect(addr string) (time.Duration, error)
	Banner(addr string) (string, time.Duration, error)
	// Auth runs nothing on host over a connection of its own, in batch mode.
	Auth(host string) error
	Master(host string) Master
	DropMaster(host string)
	ClearSocket(host string) error
	// Shared runs nothing on host over ym's shared connection, starting one when none runs.
	Shared(host string) error
	Remote(host string) (Remote, error)
	Version() string
	Install(host string) (string, error)
	Now() time.Time
}

// serveLate is how old a host's last serve cycle gets before its serve counts as stuck.
const serveLate = 3 * time.Minute

type hostDoc struct {
	host, name string
	p          HostProbes
	emit       func(Step)
	fixed      []string
}

// Host checks the way from this machine to host, named name on the board, step by step, emitting each step as it ends, fixes
// what it safely can and returns one line saying what is wrong and what to do.
func Host(host, name string, p HostProbes, emit func(Step)) string {
	if name == "" {
		name = host
	}
	d := &hostDoc{host: host, name: name, p: p, emit: emit}
	return d.run()
}

func (d *hostDoc) step(name string, st Status, took time.Duration, format string, args ...any) {
	d.emit(Step{Name: name, Status: st, Detail: fmt.Sprintf(format, args...), Took: took})
}

func (d *hostDoc) run() string {
	ips, err := d.p.Resolve(d.host)
	tunnels := d.p.Tunnels()
	if err != nil || len(ips) == 0 {
		d.step("dns", Failed, 0, "%s: %v", d.host, err)
		d.tunnelStep(tunnels, nil)
		if pub, perr := d.p.ResolvePublic(d.host); perr == nil && len(pub) > 0 {
			return fmt.Sprintf("%s does not resolve here but public DNS gives %s: this machine's DNS is broken, often a VPN's resolver; %s", d.name, pub[0], reconnectHint(tunnels))
		}
		return fmt.Sprintf("%s does not resolve anywhere: check the name (ym remote forget %s if it is gone)", d.name, d.host)
	}
	ip := first4(ips)
	d.step("dns", OK, 0, "%s is %s", d.host, strings.Join(ips, ", "))
	route, err := d.p.Route(ip)
	if err != nil {
		d.step("route", Failed, 0, "%s: %v", ip, err)
	} else {
		d.step("route", OK, 0, "%s via %s%s", ip, route.Iface, netNote(route, ip))
	}
	tun := tunnelFor(route, tunnels, ip)
	d.tunnelStep(tunnels, tun)
	took, err := d.p.Connect(net.JoinHostPort(ip, "22"))
	if err != nil {
		d.step("tcp :22", Failed, took, "%s", connErr(err))
		return d.unreachable(ip, route, tun, err)
	}
	d.step("tcp :22", OK, took, "%s:22 accepts", ip)
	banner, took, err := d.p.Banner(net.JoinHostPort(ip, "22"))
	if err != nil {
		d.step("banner", Failed, took, "%s", connErr(err))
		return fmt.Sprintf("%s takes TCP on port 22 but sends no ssh banner (%s): its ssh front end is stuck; try again in a minute, then restart the VM", d.name, connErr(err))
	}
	d.step("banner", OK, took, "%s", banner)
	if err := d.p.Auth(d.host); err != nil {
		d.step("auth", Failed, 0, "%v", err)
		return d.authVerdict(err)
	}
	d.step("auth", OK, 0, "ssh -o BatchMode=yes logs in")
	if err := d.master(); err != nil {
		return fmt.Sprintf("ssh logs in to %s on its own, but ym's shared connection still fails after a rebuild (%v): run ym remote repair %s again; if it repeats, rm /tmp/ym-%d-* and retry", d.name, err, d.host, os.Getuid())
	}
	return d.ym()
}

func (d *hostDoc) tunnelStep(tunnels []Tunnel, tun *Tunnel) {
	if len(tunnels) == 0 {
		d.step("tunnel", OK, 0, "no VPN client found")
		return
	}
	var parts []string
	for _, t := range tunnels {
		parts = append(parts, t.String())
	}
	st, note := OK, ""
	if tun != nil {
		note = "; this host's route goes through " + tun.Kind
		if !tun.Connected {
			st = Failed
		}
	}
	d.step("tunnel", st, 0, "%s%s", strings.Join(parts, "; "), note)
}

// unreachable is the verdict once the host's port 22 does not answer, telling a dead route, a dead provider and a dead VM apart.
func (d *hostDoc) unreachable(ip string, route Route, tun *Tunnel, err error) string {
	if errors.Is(err, syscall.ECONNREFUSED) {
		return fmt.Sprintf("%s refuses ssh on port 22: the VM is up but its sshd is not; restart the VM", d.name)
	}
	gates := gateways(d.host)
	down, up := 0, ""
	for _, g := range gates {
		gips, gerr := d.p.Resolve(g)
		if gerr != nil || len(gips) == 0 {
			d.step("provider", Failed, 0, "%s does not resolve: %v", g, gerr)
			down++
			continue
		}
		gip := first4(gips)
		via := ""
		if r, rerr := d.p.Route(gip); rerr == nil {
			via = " via " + r.Iface
		}
		took, cerr := d.p.Connect(net.JoinHostPort(gip, "22"))
		if cerr != nil {
			d.step("provider", Failed, took, "%s (%s)%s: %s", g, gip, via, connErr(cerr))
			down++
			continue
		}
		d.step("provider", OK, took, "%s (%s)%s answers", g, gip, via)
		if up == "" {
			up = g
		}
	}
	providerDown := len(gates) > 0 && down == len(gates)
	direct := ""
	if tun != nil {
		if pubs, perr := d.p.ResolvePublic(d.host); perr == nil {
			for _, pub := range pubs {
				if pub == ip || !isIPv4(pub) {
					continue
				}
				took, cerr := d.p.Connect(net.JoinHostPort(pub, "22"))
				if cerr != nil {
					d.step("direct", Failed, took, "public address %s:22: %s", pub, connErr(cerr))
					break
				}
				d.step("direct", OK, took, "public address %s:22 answers outside %s", pub, tun.Kind)
				direct = pub
				break
			}
		}
	}
	gate := strings.Join(gates, " and ")
	network := route.Net
	if network == "" {
		network = ip
	}
	switch {
	case tun != nil && !tun.Connected:
		return fmt.Sprintf("%s routes through %s, which is not connected: open %s and connect", d.name, tun.Kind, tun.Kind)
	case tun != nil && providerDown:
		return fmt.Sprintf("%s route to %s is down: open %s and reconnect (%s also unreachable, so this isn't your VM)", tun.Kind, network, tun.Kind, gatewaysAre(gates))
	case tun != nil && direct != "":
		return fmt.Sprintf("%s route to %s is down, but %s answers directly at %s: reconnect %s, or disconnect it to reach the VM directly", tun.Kind, network, d.name, direct, tun.Kind)
	case tun != nil && up != "":
		return fmt.Sprintf("only this VM is unreachable through %s: %s answers, so the VM or its %s resource is down; restart the VM", tun.Kind, up, tun.Kind)
	case tun != nil:
		return fmt.Sprintf("%s does not answer through %s (%s): reconnect %s; if it still fails, the VM is down", d.name, tun.Kind, network, tun.Kind)
	case providerDown:
		return fmt.Sprintf("%s unreachable too, so the whole provider or your network is down, not just this VM: check your connection and the provider's status", isAre(gate, len(gates)))
	case up != "":
		return fmt.Sprintf("just this VM: %s does not answer on port 22 while %s does; restart the VM", d.name, up)
	}
	return fmt.Sprintf("%s does not answer on port 22 (%s): check your network, then restart the VM", d.name, connErr(err))
}

func (d *hostDoc) authVerdict(err error) string {
	text := err.Error()
	switch {
	case strings.Contains(text, "Permission denied"):
		return fmt.Sprintf("%s answers but refuses your ssh key: run ssh-add -l to check your agent holds the key ~/.ssh/config names for it, then ssh %s by hand", d.name, d.host)
	case strings.Contains(text, "Host key verification failed"), strings.Contains(text, "REMOTE HOST IDENTIFICATION HAS CHANGED"):
		return fmt.Sprintf("%s's host key changed or is unknown: if the VM was rebuilt, run ssh-keygen -R %s, then ssh %s by hand once", d.name, d.host, d.host)
	}
	return fmt.Sprintf("%s answers on port 22 but ssh fails: %v; run ssh -v %s to see where", d.name, err, d.host)
}

// master checks ym's shared connection, clearing a stale socket or a master that no longer carries anything, and rebuilds it once.
func (d *hostDoc) master() error {
	m := d.p.Master(d.host)
	switch {
	case m.Socket && !m.Alive:
		if err := d.p.ClearSocket(d.host); err != nil {
			d.step("master", Failed, 0, "stale socket: %v", err)
		} else {
			d.step("master", Fixed, 0, "cleared a stale socket (%s)", m.Detail)
			d.fixed = append(d.fixed, "cleared a stale ssh socket")
		}
	case m.Alive:
		if err := d.p.Shared(d.host); err == nil {
			d.step("master", OK, 0, "shared connection carries commands (%s)", m.Detail)
			return nil
		} else {
			d.step("master", Failed, 0, "shared connection is up but carries nothing: %v", err)
		}
		d.p.DropMaster(d.host)
		d.step("master", Fixed, 0, "dropped the dead shared connection")
		d.fixed = append(d.fixed, "dropped a dead shared ssh connection")
	}
	if err := d.p.Shared(d.host); err != nil {
		d.step("master", Failed, 0, "a new shared connection fails: %v", err)
		return err
	}
	d.step("master", OK, 0, "new shared connection carries commands")
	return nil
}

func (d *hostDoc) ym() string {
	r, err := d.p.Remote(d.host)
	if err != nil {
		d.step("ym", Failed, 0, "%v", err)
		return d.done(fmt.Sprintf("ssh to %s works but running ym there fails: %v; run ym remote %s service status", d.name, err, d.host))
	}
	want := d.p.Version()
	switch {
	case !r.Installed:
		d.step("ym", Failed, 0, "not installed at ~/.local/bin/ym")
	case r.Version != want:
		d.step("ym", Failed, 0, "version %s, this machine's is %s", orUnknown(r.Version), want)
	}
	if !r.Installed || r.Version != want || !d.serveAlive(r) {
		if r.Installed && r.Version == want {
			d.step("serve", Failed, 0, "%s", d.serveState(r))
		}
		out, err := d.p.Install(d.host)
		if err != nil {
			d.step("install", Failed, 0, "%v", err)
			return d.done(fmt.Sprintf("ym on %s needs installing and ym remote install failed: %v; run ym remote install %s", d.name, err, d.host))
		}
		d.step("install", Fixed, 0, "%s", out)
		d.fixed = append(d.fixed, "installed current ym on "+d.name)
		if r, err = d.p.Remote(d.host); err != nil {
			d.step("ym", Failed, 0, "%v", err)
			return d.done(fmt.Sprintf("ym remote install on %s ran but ym there still fails: %v", d.name, err))
		}
	}
	if r.Version != want {
		d.step("ym", Failed, 0, "still version %s after installing, want %s", orUnknown(r.Version), want)
		return d.done(fmt.Sprintf("ym on %s is still %s after ym remote install: run ym remote install %s and read its output", d.name, orUnknown(r.Version), d.host))
	}
	d.step("ym", OK, 0, "version %s, same as here", r.Version)
	if !d.serveAlive(r) {
		d.step("serve", Failed, 0, "%s", d.serveState(r))
		return d.done(fmt.Sprintf("ym serve on %s %s: run ym remote %s service restart, then read ~/.local/state/yamnaya/work-service.log there", d.name, d.serveState(r), d.host))
	}
	d.step("serve", OK, 0, "%s", d.serveState(r))
	return d.done("")
}

func (d *hostDoc) serveAlive(r Remote) bool {
	return r.ServeRunning && !r.ServeAt.IsZero() && d.p.Now().Sub(r.ServeAt) < serveLate
}

func (d *hostDoc) serveState(r Remote) string {
	switch {
	case !r.ServeRunning:
		return "is not running"
	case r.ServeAt.IsZero():
		return "runs but has not finished a cycle"
	case d.p.Now().Sub(r.ServeAt) >= serveLate:
		return "runs but its last cycle was " + d.p.Now().Sub(r.ServeAt).Round(time.Second).String() + " ago"
	}
	return "running, last cycle " + d.p.Now().Sub(r.ServeAt).Round(time.Second).String() + " ago"
}

// done is the verdict problem gives, or that the host is healthy, with what the doctor fixed on the way.
func (d *hostDoc) done(problem string) string {
	if problem != "" {
		return problem
	}
	if len(d.fixed) > 0 {
		return fmt.Sprintf("%s works again: %s", d.name, strings.Join(d.fixed, ", "))
	}
	return fmt.Sprintf("%s is healthy: ssh, the shared connection, ym and its serve all answer", d.name)
}

// tunnelFor is the VPN client that carries traffic to ip when its route leaves through a tunnel interface.
func tunnelFor(r Route, tunnels []Tunnel, ip string) *Tunnel {
	if !tunnelIface(r.Iface) {
		return nil
	}
	for i, t := range tunnels {
		if slices.Contains(t.Peers, ip) {
			return &tunnels[i]
		}
	}
	for i, t := range tunnels {
		if t.Kind == "Twingate" && (t.Running || t.Connected) {
			return &tunnels[i]
		}
	}
	for i, t := range tunnels {
		if t.Running || t.Connected {
			return &tunnels[i]
		}
	}
	return &Tunnel{Kind: "the VPN on " + r.Iface, Running: true, Connected: true}
}

func tunnelIface(name string) bool {
	for _, p := range []string{"utun", "tun", "tailscale", "sdwan", "wg", "ipsec", "ppp"} {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// gateways are the provider's own ssh front ends beside a host: its domain's apex, and exe.dev for exe.dev's VMs.
func gateways(host string) []string {
	if net.ParseIP(host) != nil {
		return nil
	}
	labels := strings.Split(strings.TrimSuffix(host, "."), ".")
	if len(labels) < 3 {
		return nil
	}
	apex := strings.Join(labels[len(labels)-2:], ".")
	if apex == "exe.xyz" {
		return []string{"exe.dev", apex}
	}
	return []string{apex}
}

// gatewaysAre names the gateways' ssh front ends with their verb, as "exe.dev's ssh gateway is".
func gatewaysAre(gates []string) string {
	owners := make([]string, len(gates))
	for i, g := range gates {
		owners[i] = g + "'s"
	}
	if len(gates) == 1 {
		return owners[0] + " ssh gateway is"
	}
	return strings.Join(owners, " and ") + " ssh gateways are"
}

func isAre(subject string, n int) string {
	if n == 1 {
		return subject + " is"
	}
	return subject + " are"
}

func reconnectHint(tunnels []Tunnel) string {
	for _, t := range tunnels {
		if t.Running || t.Connected {
			return "reconnect " + t.Kind + ", or quit it to use your network's DNS"
		}
	}
	return "check your network's DNS"
}

func netNote(r Route, ip string) string {
	if r.Net == "" || r.Net == ip {
		return ""
	}
	return " (" + r.Net + ")"
}

func connErr(err error) string {
	var ne net.Error
	switch {
	case errors.Is(err, syscall.ECONNREFUSED):
		return "connection refused"
	case errors.Is(err, os.ErrDeadlineExceeded), errors.As(err, &ne) && ne.Timeout():
		return "no answer (timed out)"
	}
	return err.Error()
}

func first4(ips []string) string {
	for _, ip := range ips {
		if isIPv4(ip) {
			return ip
		}
	}
	return ips[0]
}

func isIPv4(s string) bool {
	ip := net.ParseIP(s)
	return ip != nil && ip.To4() != nil
}

func orUnknown(v string) string {
	if v == "" {
		return "unknown (too old to say)"
	}
	return v
}
