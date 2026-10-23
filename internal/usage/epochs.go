package usage

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/project-kikkuli/yamnaya/internal/profile"
)

type accountEpoch struct {
	Start   int64  `json:"start"`
	Account string `json:"account"`
}
type epochHistory struct {
	Version  int            `json:"version"`
	Observed int64          `json:"observed"`
	Epochs   []accountEpoch `json:"epochs"`
}

func epochPath(dir, path string) string {
	sum := sha256.Sum256([]byte(path))
	return filepath.Join(dir, "account-epochs", hex.EncodeToString(sum[:])+".json")
}
func readEpochs(dir, path string) (epochHistory, error) {
	var e epochHistory
	b, err := os.ReadFile(epochPath(dir, path))
	if os.IsNotExist(err) {
		return e, nil
	}
	if err != nil {
		return e, err
	}
	err = json.Unmarshal(b, &e)
	return e, err
}
func writeEpochs(dir, path string, e epochHistory) error {
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	return profile.Write(epochPath(dir, path), b)
}
func (m *Meter) accountSources(s source) ([]source, error) {
	e, err := readEpochs(m.StateDir, s.Path)
	if err != nil {
		return nil, err
	}
	changed := false
	if e.Version == 0 {
		changed = true
		account := s.Account
		var observed int64
		if old := m.files[s.Path]; old != nil {
			account = old.Account
			observed = old.Offset
		}
		e = epochHistory{Version: 1, Observed: observed, Epochs: []accountEpoch{{Start: 0, Account: account}}}
	}
	if e.Version != 1 || len(e.Epochs) == 0 {
		return nil, fmt.Errorf("unsupported account ownership provenance")
	}
	if e.Epochs[len(e.Epochs)-1].Account != s.Account {
		changed = true
		boundary := e.Observed
		for _, f := range m.files {
			if f.physical() == s.Path {
				boundary = max(boundary, f.Offset)
			}
		}
		if boundary == e.Epochs[len(e.Epochs)-1].Start && boundary == 0 {
			e.Epochs[len(e.Epochs)-1].Account = s.Account
		} else {
			e.Epochs = append(e.Epochs, accountEpoch{Start: boundary, Account: s.Account})
		}
	}
	if changed {
		if err = writeEpochs(m.StateDir, s.Path, e); err != nil {
			return nil, err
		}
	}
	var out []source
	for i, epoch := range e.Epochs {
		next := s
		next.PhysicalPath = s.Path
		next.Account = epoch.Account
		next.EpochStart = epoch.Start
		if i+1 < len(e.Epochs) {
			next.Size = min(s.Size, e.Epochs[i+1].Start)
		}
		if epoch.Start > next.Size {
			return nil, fmt.Errorf("account-owned history was truncated in %s", s.Path)
		}
		if i > 0 {
			next.Path = s.Path + "\x00epoch=" + strconv.FormatInt(epoch.Start, 10)
		}
		out = append(out, next)
	}
	return out, nil
}
func (m *Meter) observeEpochs(work []*parsed) error {
	offsets := map[string]int64{}
	for _, p := range work {
		path := p.f.physical()
		offsets[path] = max(offsets[path], p.f.Offset)
	}
	for path, offset := range offsets {
		e, err := readEpochs(m.StateDir, path)
		if err != nil {
			return err
		}
		if offset > e.Observed {
			e.Observed = offset
			if err = writeEpochs(m.StateDir, path, e); err != nil {
				return err
			}
		}
	}
	return nil
}
func (s source) physical() string {
	if s.PhysicalPath != "" {
		return s.PhysicalPath
	}
	return s.Path
}

func TransferOrigins(dir, path string, p profile.Profile, start, end int64) ([]profile.Origin, error) {
	origins, err := profile.Origins(dir, path, p, start, end)
	if err != nil {
		return nil, err
	}
	epochs, err := readEpochs(dir, path)
	if err != nil {
		return nil, err
	}
	if len(epochs.Epochs) == 0 {
		return origins, nil
	}
	var out []profile.Origin
	for _, origin := range origins {
		if origin.Path != path || origin.Root != p.Root {
			out = append(out, origin)
			continue
		}
		for i, epoch := range epochs.Epochs {
			end := origin.End
			if i+1 < len(epochs.Epochs) {
				end = min(end, epochs.Epochs[i+1].Start)
			}
			from := max(origin.Start, epoch.Start)
			if from < end {
				part := origin
				part.Start = from
				part.End = end
				part.Account = epoch.Account
				out = append(out, part)
			}
		}
	}
	return out, nil
}
