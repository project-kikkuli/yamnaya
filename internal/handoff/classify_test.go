package handoff_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/handoff"
)

func TestExtractionRequiresSourceGroundingAndCompleteEventCoverage(t *testing.T) {
	events := []handoff.Event{{ID: "one", Work: "work", Text: "Please review #42."}, {ID: "two", Work: "work", Text: "CI is running."}}
	for _, tc := range []struct {
		name, raw string
		valid     bool
	}{
		{"explicit review and no incidental task", `{"Events":[{"ID":"one","Requests":[{"Kind":"review","Target":"#42","Topic":"review-42","Question":"Review #42?","Quote":"Please review #42."}]},{"ID":"two","Requests":[]}]}`, true},
		{"omitted event", `{"Events":[{"ID":"one","Requests":[]}]}`, false},
		{"fabricated evidence", `{"Events":[{"ID":"one","Requests":[{"Kind":"review","Topic":"review","Question":"Review?","Quote":"Ready and green"}]},{"ID":"two","Requests":[]}]}`, false},
		{"owner approval", `{"Events":[{"ID":"one","Requests":[{"Kind":"approve","Topic":"approval","Question":"Approved","Quote":"Please review #42."}]},{"ID":"two","Requests":[]}]}`, false},
		{"duplicate identity", `{"Events":[{"ID":"one","Requests":[]},{"ID":"one","Requests":[]}]}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			claims, err := handoff.Parse(events, []byte(tc.raw))
			if (err == nil) != tc.valid {
				t.Fatalf("claims %+v err %v", claims, err)
			}
		})
	}
}

func TestRequesterIsTheHumanNamedInTheSourceEvent(t *testing.T) {
	events := []handoff.Event{{ID: "one", Work: "work", Text: "Waiting on you: review the key decisions doc. Brian asked in today's sync."}}
	for _, tc := range []struct {
		name, requester string
		valid           bool
	}{
		{"named human", "Brian", true},
		{"agent's own request", "", true},
		{"name absent from the event", "Dana", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := fmt.Sprintf(`{"Events":[{"ID":"one","Requests":[{"Kind":"review","Topic":"key decisions","Question":"Review the doc?","Quote":"review the key decisions doc","Requester":%q}]}]}`, tc.requester)
			claims, err := handoff.Parse(events, []byte(raw))
			if (err == nil) != tc.valid {
				t.Fatalf("claims %+v err %v", claims, err)
			}
			if tc.valid && claims[0].Requester != tc.requester {
				t.Fatalf("requester %q", claims[0].Requester)
			}
		})
	}
}

func TestNativeSemanticHandoffExtraction(t *testing.T) {
	if os.Getenv("YAMNAYA_HANDOFF_NATIVE_TEST") == "" {
		t.Skip("explicit native-model acceptance run")
	}
	harness := os.Getenv("YAMNAYA_HANDOFF_NATIVE_TEST")
	if harness == "1" {
		harness = "claude"
	}
	bin, err := exec.LookPath(harness)
	if err != nil {
		t.Fatal(err)
	}
	events := []handoff.Event{
		{ID: "handoff", Work: "work", Session: "lead", Agent: "lead", Text: "#42 (repository checks) is ready for your review. Its shepherd is stopped.\n#43 (advisory checks) is green and ready for your review, but eight review threads are still unresolved.\n#44 has CI running on its new head.\nNext: reply go A+B to start the flake fixes and overhead cuts."},
		{ID: "chat", Work: "work", Session: "lead", Agent: "lead", Text: "The remote login dialog needs a code from you. Switch to this chat and enter it; I cannot do it."},
		{ID: "quotation", Work: "work", Session: "lead", Agent: "lead", Text: "Here is an example of a message: 'PR #99 is ready for your review.' This is only a quotation for documentation, not a real review handoff."},
	}
	ledger := map[string]any{"Repository": "example/query", "ExistingItems": []map[string]string{{"ID": "item:#7", "Kind": "go", "Question": "Start flake fixes and overhead cuts A+B?", "State": "answered", "Answer": "Go A+B now"}}}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	claims, err := (handoff.Native{Harness: harness, Binary: bin, Directory: t.TempDir()}).Classify(ctx, events, ledger)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(claims)
	t.Log(string(raw))
	reviews := map[string]bool{}
	answered := false
	chat := false
	for _, c := range claims {
		if c.Event.ID == "quotation" {
			t.Fatal("quoted example became an owner obligation")
		}
		if c.Kind == "chat" && c.Event.Session == "lead" {
			chat = true
		}
		if c.Kind == "review" {
			reviews[c.Target] = true
		}
		if c.Kind == "authorization" && c.Existing == "item:#7" {
			answered = true
		}
	}
	if len(reviews) != 2 || !reviews["#42"] || !reviews["#43"] || !answered || !chat {
		t.Fatalf("semantic requests not recovered: %s", raw)
	}
}
