package mcp

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
)

type Server struct {
	Name          string    `json:"name"`
	Transport     string    `json:"transport"`
	Scope         string    `json:"scope"`
	Source        string    `json:"source"`
	Context       string    `json:"context,omitempty"`
	Configuration string    `json:"configuration"`
	Approval      string    `json:"approval"`
	Effective     bool      `json:"effective"`
	Mutable       bool      `json:"mutable"`
	Auth          string    `json:"auth"`
	Health        string    `json:"health"`
	CheckedAt     time.Time `json:"checked_at,omitzero"`
	Fingerprint   string    `json:"revision"`
}

type Inventory struct {
	Target  Target   `json:"target"`
	Servers []Server `json:"servers"`
	Notes   []string `json:"notes"`
}

func readObject(path string, toml bool) (map[string]any, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return map[string]any{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("cannot read native MCP configuration")
	}
	var d map[string]any
	if toml {
		err = tomllib(b, &d)
	} else {
		err = json.Unmarshal(b, &d)
	}
	if err != nil {
		return nil, fmt.Errorf("invalid native MCP configuration (content withheld)")
	}
	return d, nil
}

func tomllib(b []byte, out any) error { return toml.Unmarshal(b, out) }
func object(v any) map[string]any     { d, _ := v.(map[string]any); return d }
func textValue(v any) string          { s, _ := v.(string); return s }
func contains(v any, name string) bool {
	if list, ok := v.([]any); ok {
		for _, x := range list {
			if x == name {
				return true
			}
		}
	}
	return false
}

func projectRoot(cwd string) string {
	for dir := cwd; ; dir = filepath.Dir(dir) {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir
		}
		if filepath.Dir(dir) == dir {
			return cwd
		}
	}
}

func (i *Inventory) appendServers(d map[string]any, scope, path, context string, mutable bool) {
	for name, raw := range d {
		spec := object(raw)
		transport := textValue(spec["type"])
		if transport == "" {
			if _, ok := spec["command"]; ok {
				transport = "stdio"
			} else if _, ok := spec["url"]; ok {
				transport = "http"
			} else {
				transport = "unknown"
			}
		}
		b, _ := json.Marshal(raw)
		s := Server{Name: name, Scope: scope, Source: path, Context: context, Transport: transport, Configuration: "configured", Approval: "not-required", Effective: true, Mutable: mutable, Auth: "unknown", Health: "unchecked", Fingerprint: fmt.Sprintf("%x", sha256.Sum256(b))}
		if spec == nil {
			s.Configuration, s.Mutable = "unsupported", false
		}
		if enabled, ok := spec["enabled"].(bool); ok && !enabled {
			s.Configuration = "disabled"
		}
		for n := range i.Servers {
			if i.Servers[n].Name == name {
				i.Servers[n].Effective = false
			}
		}
		i.Servers = append(i.Servers, s)
	}
}

func Read(t Target) (Inventory, error) {
	i := Inventory{Target: t, Servers: []Server{}, Notes: []string{"Passive file inventory; authentication and connectivity require an explicit check.", "Plugin, cloud and managed native sources may add or restrict servers; unenumerated here. Use the native MCP screen for their full inventory and approval."}}
	if t.Harness == "claude" {
		return readClaude(i)
	}
	return readCodex(i)
}

func readClaude(i Inventory) (Inventory, error) {
	t := i.Target
	d, err := readObject(t.ConfigFile, false)
	if err != nil {
		return i, err
	}
	i.appendServers(object(d["mcpServers"]), "user", t.ConfigFile, "", true)
	root := projectRoot(t.Cwd)
	p := filepath.Join(root, ".mcp.json")
	project, err := readObject(p, false)
	if err != nil {
		return i, err
	}
	i.appendServers(object(project["mcpServers"]), "project", p, root, true)
	entry := object(object(d["projects"])[t.Cwd])
	i.appendServers(object(entry["mcpServers"]), "local", t.ConfigFile, t.Cwd, true)
	settings := []map[string]any{}
	for _, path := range []string{filepath.Join(t.Root, "settings.json"), filepath.Join(root, ".claude", "settings.json"), filepath.Join(root, ".claude", "settings.local.json")} {
		setting, err := readObject(path, false)
		if err != nil {
			return i, err
		}
		settings = append(settings, setting)
	}
	for n := range i.Servers {
		s := &i.Servers[n]
		if s.Scope == "project" {
			s.Approval = "required"
			if contains(entry["enabledMcpjsonServers"], s.Name) {
				s.Approval = "approved"
			}
			if contains(entry["disabledMcpjsonServers"], s.Name) {
				s.Approval = "rejected"
			}
		}
		if contains(entry["disabledMcpServers"], s.Name) {
			s.Configuration = "disabled"
		}
		for _, setting := range settings {
			if s.Scope == "project" {
				if setting["enableAllProjectMcpServers"] == true || contains(setting["enabledMcpjsonServers"], s.Name) {
					s.Approval = "approved"
				}
				if contains(setting["disabledMcpjsonServers"], s.Name) {
					s.Approval = "rejected"
				}
			}
		}
	}
	i.sort()
	return i, nil
}

func readCodex(i Inventory) (Inventory, error) {
	t := i.Target
	d, err := readObject(t.ConfigFile, true)
	if err != nil {
		return i, err
	}
	i.appendServers(object(d["mcp_servers"]), "user", t.ConfigFile, "", true)
	root := projectRoot(t.Cwd)
	dirs := []string{}
	for dir := t.Cwd; ; dir = filepath.Dir(dir) {
		dirs = append(dirs, dir)
		if dir == root {
			break
		}
	}
	slices.Reverse(dirs)
	trusted := false
	for path, raw := range object(d["projects"]) {
		if textValue(object(raw)["trust_level"]) == "trusted" && (t.Cwd == path || strings.HasPrefix(t.Cwd, filepath.Clean(path)+string(os.PathSeparator))) {
			trusted = true
		}
	}
	for _, dir := range dirs {
		path := filepath.Join(dir, ".codex", "config.toml")
		if path == t.ConfigFile {
			continue
		}
		doc, err := readObject(path, true)
		if err != nil {
			return i, err
		}
		before := len(i.Servers)
		if trusted {
			i.appendServers(object(doc["mcp_servers"]), "project", path, dir, false)
		} else {
			old := append([]Server(nil), i.Servers...)
			i.appendServers(object(doc["mcp_servers"]), "project", path, dir, false)
			copy(i.Servers, old)
			for n := before; n < len(i.Servers); n++ {
				i.Servers[n].Effective = false
				i.Servers[n].Approval = "native-trust-required"
			}
		}
	}
	i.Notes = append(i.Notes, "Codex project layers are read-only here; its native MCP add/remove commands change user scope only. Native policy and active config profiles can further affect effective configuration.")
	i.sort()
	return i, nil
}

func (i *Inventory) sort() {
	sort.Slice(i.Servers, func(a, b int) bool {
		x, y := i.Servers[a], i.Servers[b]
		return x.Name+"\x00"+x.Scope+"\x00"+x.Source < y.Name+"\x00"+y.Scope+"\x00"+y.Source
	})
}

func (i Inventory) Select(name, scope, source string) (Server, error) {
	var matches []Server
	for _, s := range i.Servers {
		if s.Name == name && s.Scope == scope && (source == "" || s.Source == source) {
			matches = append(matches, s)
		}
	}
	if len(matches) != 1 {
		return Server{}, fmt.Errorf("select one configured server with its exact --scope and --source from ym mcp list")
	}
	return matches[0], nil
}
