package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/core"
)

func TestLinearPollRetainsCompleteCacheOnPartialPage(t *testing.T) {
	dir := t.TempDir()
	st, err := core.OpenStore(filepath.Join(dir, "ym.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	at := time.Now().Add(-time.Hour)
	previous := core.LinearSnapshot{OwnerID: "owner", WorkspaceID: "org", At: at, Issues: []core.LinearIssue{{ID: "old", Identifier: "ENG-1", Title: "keep evidence"}}}
	if err = st.SyncLinear(previous); err != nil {
		t.Fatal(err)
	}
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var req struct{ Query string }
		json.NewDecoder(r.Body).Decode(&req)
		if strings.Contains(req.Query, "YamnayaViewer") {
			fmt.Fprint(w, `{"data":{"viewer":{"id":"owner","organization":{"id":"org"}}}}`)
			return
		}
		fmt.Fprint(w, `{"data":{"issues":{"nodes":[{"id":"new","identifier":"ENG-2"}],"pageInfo":{"hasNextPage":true}}}}`)
	}))
	defer server.Close()
	poll := linearPoll{dir: dir, every: 5 * time.Minute, api: server.URL, token: func(string, linearConfig) (string, error) { return "secret", nil }}
	if err = poll.poll(context.Background(), st, true); err == nil {
		t.Fatal("accepted incomplete page")
	}
	got, err := st.LinearSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Issues) != 1 || got.Issues[0].ID != "old" || got.Error == "" || !got.At.Equal(previous.At) {
		t.Fatalf("failed read lost evidence: %+v", got)
	}
	if err = poll.poll(context.Background(), st, false); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("read cadence ignored: %d requests", calls)
	}
}

func TestLinearFileCredentialPermissionsAndEnvironmentOverride(t *testing.T) {
	dir := t.TempDir()
	cfg := linearConfig{OwnerID: "owner", Storage: "file"}
	t.Setenv("LINEAR_API_KEY", "")
	path := filepath.Join(dir, "linear.token")
	if err := os.WriteFile(path, []byte("file-secret"), 0600); err != nil {
		t.Fatal(err)
	}
	token, err := loadLinearToken(dir, cfg)
	if err != nil || token != "file-secret" {
		t.Fatalf("private credential refused: %v", err)
	}
	if err = os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err = loadLinearToken(dir, cfg); err == nil {
		t.Fatal("accepted world-readable credential")
	}
	t.Setenv("LINEAR_API_KEY", "environment-secret")
	token, err = loadLinearToken(dir, cfg)
	if err != nil || token != "environment-secret" {
		t.Fatal("explicit environment credential was not used")
	}
}
