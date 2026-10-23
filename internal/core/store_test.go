package core

import (
	"database/sql"
	"path/filepath"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestOldPRTableMigratesOnceWhenManyProcessesOpenIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ym.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`CREATE TABLE ym_prs (url TEXT PRIMARY KEY, state TEXT NOT NULL, title TEXT NOT NULL DEFAULT '', review TEXT NOT NULL DEFAULT '',
			passed INTEGER NOT NULL DEFAULT 0, failed INTEGER NOT NULL DEFAULT 0, pending INTEGER NOT NULL DEFAULT 0, fetched_at TEXT NOT NULL)`,
		`INSERT INTO ym_prs (url, state, fetched_at) VALUES ('https://github.com/o/r/pull/1', 'OPEN', '2026-10-01T00:00:00Z')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()
	var wg sync.WaitGroup
	errs := make([]error, 8)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			st, err := OpenStore(path)
			if err == nil {
				err = st.Close()
			}
			errs[i] = err
		}()
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	st, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if known, _ := st.PRStatuses(); len(known) != 0 {
		t.Fatalf("old statuses kept: %v", known)
	}
	want := PRStatus{State: "OPEN", Head: "feat", Commits: 3, Threads: 2, Unresolved: 1, Fetched: time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)}
	if err := st.SavePRStatus("u", want); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.PRStatuses(); got["u"] != want {
		t.Fatalf("got %+v, want %+v", got["u"], want)
	}
}

func TestExistingReviewRequestsKeepTheirStateAndGainContextOnRefresh(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ym.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`CREATE TABLE ym_review_requests (url TEXT PRIMARY KEY, updated TEXT NOT NULL DEFAULT '', state TEXT NOT NULL, seen_at TEXT NOT NULL)`,
		`INSERT INTO ym_review_requests VALUES ('open', '1', 'open', '2026-10-01T00:00:00Z'), ('gone', '1', 'gone', '2026-10-01T00:00:00Z')`,
	} {
		if _, err := db.Exec(q); err != nil {
			db.Close()
			t.Fatal(err)
		}
	}
	db.Close()
	for range 2 {
		st, err := OpenStore(path)
		if err != nil {
			t.Fatal(err)
		}
		reviews, err := st.OpenReviews()
		st.Close()
		if err != nil || len(reviews) != 1 || reviews[0].URL != "open" {
			t.Fatalf("review requests changed during migration: %+v, %v", reviews, err)
		}
	}
	st, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	want := ReviewRequest{URL: "open", Updated: "2", Title: "Read CSV", Author: "mira"}
	if err := st.SyncReviews([]ReviewRequest{want}); err != nil {
		t.Fatal(err)
	}
	if got, err := st.OpenReviews(); err != nil || len(got) != 1 || got[0] != want {
		t.Fatalf("refreshed review requests = %+v, %v; want %+v", got, err, want)
	}
}
