package gh

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"

	"github.com/project-kikkuli/yamnaya/internal/core"
)

func rest(endpoint string) ([]byte, error) {
	out, err := exec.Command("gh", "api", endpoint).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("GitHub %s: %w: %s", endpoint, err, strings.TrimSpace(string(out)))
	}
	return out, nil
}

func (s *Source) PRStatus(url string) (core.PRStatus, error) {
	if s.REST == nil {
		return s.details(url)
	}
	prs := core.PRsIn(url)
	if len(prs) != 1 {
		return core.PRStatus{}, fmt.Errorf("invalid pull request URL %s", url)
	}
	viewer, err := s.account()
	if err != nil {
		return core.PRStatus{}, err
	}
	out, err := s.REST(fmt.Sprintf("repos/%s/pulls/%d", prs[0].Repo, prs[0].Number))
	if err != nil {
		return core.PRStatus{}, err
	}
	var pr struct {
		State, Title  string
		Merged, Draft bool
		User          struct{ Login string }
		Head          struct{ SHA, Ref string }
		Commits       int
	}
	if err = json.Unmarshal(out, &pr); err != nil {
		return core.PRStatus{}, err
	}
	st := core.PRStatus{State: strings.ToUpper(pr.State), Title: pr.Title, Head: pr.Head.Ref, Commits: pr.Commits, Fetched: s.Now(), Merge: core.PRMergeFacts{Author: pr.User.Login, Viewer: viewer, OID: pr.Head.SHA, Draft: pr.Draft}}
	if pr.Merged {
		st.State = "MERGED"
	}
	if st.Closed() {
		return st, nil
	}
	details, err := s.details(url)
	if err != nil {
		st.Merge.Error = err.Error()
		return st, nil
	}
	if details.Closed() {
		details.Merge.Author, details.Merge.Viewer = st.Merge.Author, st.Merge.Viewer
		return details, nil
	}
	if details.Merge.OID != st.Merge.OID {
		st.Merge.Error = "Pull request head changed while reading readiness"
		return st, nil
	}
	details.Merge.Author, details.Merge.Viewer = st.Merge.Author, st.Merge.Viewer
	return details, nil
}

func (s *Source) account() (string, error) {
	s.viewerMu.Lock()
	defer s.viewerMu.Unlock()
	if s.viewer != "" {
		return s.viewer, nil
	}
	out, err := s.REST("user")
	if err != nil {
		return "", err
	}
	var user struct{ Login string }
	if err = json.Unmarshal(out, &user); err != nil {
		return "", err
	}
	if user.Login == "" {
		return "", fmt.Errorf("GitHub did not identify the authenticated account")
	}
	s.viewer = user.Login
	return s.viewer, nil
}
