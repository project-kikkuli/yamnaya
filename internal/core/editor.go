package core

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// EditorCommand opens path in the owner's $VISUAL or $EDITOR, else vi.
func EditorCommand(path string) *exec.Cmd {
	editor := os.Getenv("VISUAL")
	if editor == "" {
		editor = os.Getenv("EDITOR")
	}
	if editor == "" {
		editor = "vi"
	}
	cmd := exec.Command("sh", "-c", editor+` "$1"`, "sh", path)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd
}

const planMark = "#ym "

// PlanDraft is the file the owner edits to steer m: its plan now, under marked lines ParsePlan drops.
func PlanDraft(m Manager, text string, revision int) string {
	return fmt.Sprintf(planMark+"Steer %s (%s). Edit what it is about below and save; this saves revision %d.\n"+planMark+"Lines starting with %q are dropped. Save it unchanged or empty to steer nothing.\n%s\n",
		m.ID, m.Name, revision+1, planMark, text)
}

// ParsePlan is the plan in an edited PlanDraft, without its marked lines.
func ParsePlan(raw string) string {
	var keep []string
	for _, line := range strings.Split(raw, "\n") {
		if !strings.HasPrefix(line, planMark) {
			keep = append(keep, line)
		}
	}
	return strings.TrimSpace(strings.Join(keep, "\n"))
}

// EditPlan writes m's plan to a draft at path for an editor; ReadPlan reads back what the owner saved, "" when nothing changed.
func EditPlan(path string, m Manager, text string, revision int) error {
	return os.WriteFile(path, []byte(PlanDraft(m, text, revision)), 0o600)
}

func ReadPlan(path, was string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	plan := ParsePlan(string(raw))
	if plan == strings.TrimSpace(was) {
		return "", nil
	}
	return plan, nil
}
