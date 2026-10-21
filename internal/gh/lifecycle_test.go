package gh

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestLifecycleAndAuthorshipRemainKnownWithoutGraphQL(t *testing.T) {
	for _, tc := range []struct {
		name, state, author, want string
		merged                    bool
		wantDetails               int
	}{
		{"merged owner PR", "closed", "mira", "MERGED", true, 0},
		{"closed owner PR", "closed", "mira", "CLOSED", false, 0},
		{"open owner PR", "open", "mira", "OPEN", false, 1},
		{"open colleague PR", "open", "sol", "OPEN", false, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls, userCalls := 0, 0
			s := &Source{Now: time.Now, Run: func(string) ([]byte, error) { calls++; return nil, errors.New("GraphQL quota exhausted") }, REST: func(endpoint string) ([]byte, error) {
				if endpoint == "user" {
					userCalls++
					return []byte(`{"login":"mira"}`), nil
				}
				if endpoint != "repos/me/app/pulls/7" {
					t.Fatalf("endpoint %s", endpoint)
				}
				merged := "false"
				if tc.merged {
					merged = "true"
				}
				return []byte(`{"state":"` + tc.state + `","merged":` + merged + `,"title":"Caller migration","user":{"login":"` + tc.author + `"},"head":{"sha":"head1"}}`), nil
			}}
			for range 2 {
				st, err := s.PRStatus(url)
				if err != nil || st.State != tc.want || st.Merge.Author != tc.author || st.Merge.Viewer != "mira" || st.OwnedPR() != (tc.author == "mira") {
					t.Fatalf("status %+v err %v", st, err)
				}
				if tc.wantDetails > 0 && !strings.Contains(st.Merge.Error, "quota") {
					t.Fatalf("readiness failure missing: %+v", st)
				}
			}
			if calls != tc.wantDetails*2 || userCalls != 1 {
				t.Fatalf("detail calls %d viewer calls %d", calls, userCalls)
			}
		})
	}
}

func TestReadinessIsBoundToTheRESTHeadAndAccount(t *testing.T) {
	for _, head := range []string{"head1", "head2"} {
		t.Run(head, func(t *testing.T) {
			s := &Source{Now: time.Now, REST: func(endpoint string) ([]byte, error) {
				if endpoint == "user" {
					return []byte(`{"login":"Mira"}`), nil
				}
				return []byte(`{"state":"open","user":{"login":"mira"},"head":{"sha":"head1"}}`), nil
			}, Run: func(string) ([]byte, error) {
				return []byte(`{"data":{"resource":{"state":"OPEN","headRefOid":"` + head + `","mergeable":"MERGEABLE","mergeStateStatus":"CLEAN","reviewDecision":"APPROVED"}}}`), nil
			}}
			st, err := s.PRStatus(url)
			if err != nil || !st.OwnedPR() || st.ReadyToMerge() != (head == "head1") {
				t.Fatalf("status %+v err %v", st, err)
			}
		})
	}
}

func TestAccountLookupFailureDoesNotPoisonFutureStatusRefreshes(t *testing.T) {
	calls := 0
	s := &Source{Now: time.Now, REST: func(endpoint string) ([]byte, error) {
		if endpoint == "user" {
			calls++
			if calls == 1 {
				return nil, errors.New("not signed in")
			}
			return []byte(`{"login":"mira"}`), nil
		}
		return []byte(`{"state":"closed","merged":true,"user":{"login":"mira"},"head":{"sha":"head1"}}`), nil
	}}
	if _, err := s.PRStatus(url); err == nil {
		t.Fatal("account failure hidden")
	}
	st, err := s.PRStatus(url)
	if err != nil || !st.OwnedPR() || st.State != "MERGED" {
		t.Fatalf("status did not recover: %+v %v", st, err)
	}
}
