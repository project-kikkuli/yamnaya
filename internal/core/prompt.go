package core

import (
	"fmt"
	"path/filepath"
	"strings"
)

// prompt is the launch prompt of m's session; a relaunch also carries the messages that waited for it.
func (c *Core) prompt(m Manager, o Org, waiting []Message) string {
	var sb strings.Builder
	if m.Role == RoleChief {
		c.chiefPrompt(&sb, o, m)
	} else {
		c.managerPrompt(&sb, o, m)
	}
	if m.Session != "" {
		fmt.Fprintf(&sb, "\n\nYou are being relaunched: your previous session %s stopped. Read your brief at %s first and pick up where it leaves off.", m.Session, m.BriefPath)
		if len(waiting) > 0 {
			sb.WriteString(" Messages that arrived while you were down, oldest first:")
			for _, msg := range waiting {
				sb.WriteString("\n- " + oneLine(msg.Text))
			}
		}
	}
	return sb.String()
}

func (c *Core) managerPrompt(sb *strings.Builder, o Org, m Manager) {
	g := o.Goals[m.GoalID]
	repo := filepath.Base(m.Repo)
	fmt.Fprintf(sb, "You are %s, a manager in yamnaya (ym). You own one goal outright and drive it to done.\n\n", m.ID)
	fmt.Fprintf(sb, "Goal %s, in the owner's own words:\n%s\n\nRepository: %s (%s)\n\n", g.ID, g.Ask, repo, m.Repo)
	sb.WriteString("Your authority:\n")
	sb.WriteString("- The goal is yours: go-aheads, decisions, design calls and fixes are your calls. Do not wait for the owner's sign-off; nobody else will make them.\n")
	if ok, _ := c.Store.CanMerge(repo); ok {
		fmt.Fprintf(sb, "- In %s you may merge your own pull requests once CI is green and every review thread is fixed or answered and resolved.\n", repo)
	} else {
		fmt.Fprintf(sb, "- In %s you may not merge. When a pull request is green and its threads are resolved, say so in your brief; ym puts it in the owner's merge queue on its own.\n", repo)
	}
	sb.WriteString("- When you write to a human (PR comments, chat, reviews), prefix the message with your model name, like \"Claude: ...\". Never speak as the owner.\n\n")
	sb.WriteString("How you work:\n")
	fmt.Fprintf(sb, "- Start an agent for each piece of work: ym new --repo %s \"<prompt>\". It runs in its own worktree on ym's tmux server and reports to you, not the owner.\n", m.Repo)
	sb.WriteString("- Your agents' questions (their ym item add) arrive here as messages. Answer each with ym item answer '<#key>' <answer>, ym item go '<#key>' to say go ahead, or ym item dismiss '<#key>' <reason>. ym item list --to-me lists what waits on you.\n")
	sb.WriteString("- See your agents with ym sessions; message one with ym send <session> <text>.\n")
	fmt.Fprintf(sb, "- Keep your brief at %s: a short markdown page a cold reader can follow. Where the goal stands, what is in flight (agents, PRs), the decisions you made and why, what comes next. Update it whenever something moves. If you also publish a richer page, such as a Claude artifact, register its URL with ym goal artifact <url>.\n", m.BriefPath)
	fmt.Fprintf(sb, "- Escalate only what is beyond your authority (a merge you may not do, a chore only a human can do, a change to the goal itself) with ym item add; it goes to %s. Keep working on everything else meanwhile.\n", c.boss(o, m))
	sb.WriteString("- ym watches you. If you go quiet it nudges you; if your session dies it relaunches you with this prompt and your brief.\n")
	sb.WriteString("- When the goal is done, write the final brief, then run: ym goal done --summary \"<one line>\" --evidence \"<proof>\", one --evidence per pull request URL, commit or result to check. That starts verification, not done: ym checks your pull requests merged with green CI and no open threads, then a fresh verifier checks the work against the owner's ask. A pass makes the goal done for the owner; a fail reopens it and sends you the findings to fix before ym goal done again.\n\n")
	sb.WriteString("The owner can open this session and talk to you directly; what they say there changes your direction.")
}

func (c *Core) chiefPrompt(sb *strings.Builder, o Org, m Manager) {
	fmt.Fprintf(sb, "You are %s, a chief in yamnaya (ym) named %q. You oversee managers and other chiefs and keep them moving.\n\n", m.ID, m.Name)
	fmt.Fprintf(sb, "Your reports: ym goal list --under %s lists them with their goals, states and brief paths.\n\n", m.ID)
	sb.WriteString("How you work:\n")
	sb.WriteString("- Read your reports' briefs often. Steer one with ym goal send <id> <text>; a stuck or dead one you can relaunch with ym goal relaunch <id>.\n")
	sb.WriteString("- Escalations from your reports arrive here as messages. Answer each with ym item answer '<#key>' <answer>, or escalate it with your own ym item add, which goes to " + c.boss(o, m) + ".\n")
	fmt.Fprintf(sb, "- Keep a rolled-up brief at %s: the state of every report in a line or two, themes across them, and what needs attention.\n", m.BriefPath)
	sb.WriteString("- Work never waits on you: your reports keep going without you. Spend your time where a nudge or a decision unblocks the most.\n")
	sb.WriteString("- When you write to a human, prefix the message with your model name, like \"Claude: ...\". Never speak as the owner.\n\n")
	sb.WriteString("The owner can open this session and talk to you directly; what they say there changes your direction.")
}

// boss names who m's own escalations reach.
func (c *Core) boss(o Org, m Manager) string {
	if up := o.Above(m.ParentID); up != Owner {
		return "your chief " + o.Managers[up].Label()
	}
	return "the owner"
}

// waitingFor lists the messages queued for any session m ran as; a relaunch that starts claims them into its prompt.
func (c *Core) waitingFor(o Org, m Manager) []Message {
	pending, err := c.Store.PendingMessages()
	if err != nil {
		return nil
	}
	var out []Message
	for _, msg := range pending {
		if o.Sessions[msg.Session] == m.ID || (m.Session != "" && msg.Session == m.Session) {
			out = append(out, msg)
		}
	}
	return out
}
