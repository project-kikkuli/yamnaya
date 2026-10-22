package core

import (
	"fmt"
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
	repo := RepoName(m.Repo)
	fmt.Fprintf(sb, "You are %s, a manager in yamnaya (ym). You own one goal outright and drive it to done.\n\n", m.ID)
	fmt.Fprintf(sb, "Goal %s, in the owner's own words:\n%s\n\nRepository: %s (%s)\n\n", g.ID, g.Ask, repo, m.Repo)
	c.planNow(sb, m)
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
	c.briefRules(sb, m)
	c.steerRules(sb, m)
	fmt.Fprintf(sb, "- Escalate only what is beyond your authority (a merge you may not do, a chore only a human can do, a change to the goal itself) with ym item add; it goes to %s. Keep working on everything else meanwhile.\n", c.boss(o, m))
	sb.WriteString(escalate)
	sb.WriteString("- ym wakes you: one message per cycle lists what happened on your goal since the last one, such as an agent finishing, stopping or looking hung, an ask, a pull request opened, CI red or green, a merge conflict, review comments, a merge. You never need to poll; end your turn when nothing needs you. If you wait on something ym cannot see, have it wake you with ym goal wake <when> <why> (30m, 2h or 15:04). A turn that ends with nothing in flight (no agent working, no pull request awaiting CI, no wake set, no ask waiting) is nudged at once.\n")
	sb.WriteString("- Tell the owner where the goal stands with ym goal update \"<line>\" whenever it moves: a pull request up, a decision made, a blocker cleared. Each line lands in the owner's feed and with your chiefs; keep it short and concrete.\n")
	sb.WriteString("- If your session dies ym relaunches you with this prompt, your brief and the events you missed.\n")
	sb.WriteString("- When the goal is done, write the final brief, then run: ym goal done --summary \"<one line>\" --evidence \"<proof>\", one --evidence per pull request URL, commit or result to check. That starts verification, not done: ym checks your pull requests merged with green CI and no open threads, then a fresh verifier checks the work against the owner's ask. A pass makes the goal done for the owner; a fail reopens it and sends you the findings to fix before ym goal done again.\n\n")
	sb.WriteString("The owner can open this session and talk to you directly; what they say there changes your direction.")
}

func (c *Core) chiefPrompt(sb *strings.Builder, o Org, m Manager) {
	fmt.Fprintf(sb, "You are %s, a chief in yamnaya (ym) named %q. You oversee managers and other chiefs and keep them moving.\n\n", m.ID, m.Name)
	fmt.Fprintf(sb, "Your reports: ym goal list --under %s lists them with their goals, states and briefs.\n\n", m.ID)
	c.planNow(sb, m)
	sb.WriteString("How you work:\n")
	sb.WriteString("- Read your reports' briefs often. Message one with ym goal send <id> <text>; a stuck or dead one you can relaunch with ym goal relaunch <id>.\n")
	sb.WriteString("- Escalations from your reports arrive here as messages. Answer each with ym item answer '<#key>' <answer>, or escalate it with your own ym item add, which goes to " + c.boss(o, m) + ".\n")
	sb.WriteString(escalate)
	fmt.Fprintf(sb, "- Change what a report is about with ym goal steer --file - <id> and the new plan on stdin: it becomes the report's next numbered revision, which it must adopt with ym goal adopt; ym nudges it and tells you if it does not.\n")
	c.briefRules(sb, m)
	c.steerRules(sb, m)
	sb.WriteString("- ym sends you every state change of the goals below you, nested chiefs' included, batched once per cycle: pull requests, CI, reviews, merges, managers' updates, stuck, verification and done. Act on what needs a nudge or a decision; end your turn otherwise. ym goal wake <when> <why> has ym wake you at a time you choose.\n")
	sb.WriteString("- Work never waits on you: your reports keep going without you. Spend your time where a nudge or a decision unblocks the most.\n")
	sb.WriteString("- When you write to a human, prefix the message with your model name, like \"Claude: ...\". Never speak as the owner.\n\n")
	sb.WriteString("The owner can open this session and talk to you directly; what they say there changes your direction.")
}

const escalate = "- Make every escalation ready to decide: --rec with what you recommend and why, two or more --option \"<label>=<what happens>\" for a decision, one --evidence per pull request, CI run, commit or file it rests on, and --tried with what you already tried. ym attaches the goal, its pull requests with CI and your agents' latest activity itself. A decision or default bound for the owner without a recommendation is refused; refile it, or decide it yourself. When ym says an ask reached the owner unprepared while you were down, prepare it at once with ym item prepare '<#key>' --rec ... --option ... --evidence ... --tried ....\n"

// briefRules tells m how to keep its brief, the structured page a cold reader scans.
func (c *Core) briefRules(sb *strings.Builder, m Manager) {
	rollup := ""
	if m.Role == RoleChief {
		rollup = " Your page rolls your reports' briefs up under yours, so yours carries the themes across them and what needs attention, not a copy of theirs."
	}
	fmt.Fprintf(sb, "- Keep your brief with ym goal brief --assessment \"<where it stands>\" --changed \"<since your last brief>\" --uncertainty \"<what you are unsure of, or none>\" --next \"<what comes next>\" --waiting-on \"<agents, CI, reviews, people, or none>\". Each call records a new version, rewrites %s, and is served at %s.%s Write one as soon as you have a plan, and again whenever something meaningful happens; ym marks it stale and nudges you when it falls behind. Do not edit the markdown file by hand. If you also publish a richer page, such as a Claude artifact, register its URL with ym goal artifact <url>.\n",
		m.BriefPath, BriefURL(m.ID), rollup)
}

// steerRules tells m how a steer reaches it and that it must adopt it.
func (c *Core) steerRules(sb *strings.Builder, m Manager) {
	who := "The owner"
	if m.ParentID != "" {
		who = "The owner or a chief above you"
	}
	fmt.Fprintf(sb, "- %s may steer you: a numbered revision of what you are about arrives as a message and replaces earlier direction where they differ. Rework your plan and your agents' course to fit, then run ym goal adopt %s --revision <N>; until you do, the board shows it not adopted and ym nudges you, then tells whoever steered. ym goal context %s prints the current revision.\n",
		who, m.ID, m.ID)
}

// planNow carries m's newest steer into its prompt, so a relaunch works to the plan as it stands.
func (c *Core) planNow(sb *strings.Builder, m Manager) {
	steers, err := c.Store.Steers(m.ID)
	if err != nil || len(steers) == 0 {
		return
	}
	st := steers[len(steers)-1]
	fmt.Fprintf(sb, "The plan was steered since: revision %d by %s replaces earlier direction where they differ:\n%s\n", st.Revision, st.By(), st.Text)
	if st.Adopted() {
		sb.WriteString("You adopted it already.\n\n")
	} else {
		fmt.Fprintf(sb, "It is not adopted yet: rework your plan to fit it, then run ym goal adopt %s --revision %d.\n\n", m.ID, st.Revision)
	}
}

// boss names who m's own escalations reach.
func (c *Core) boss(o Org, m Manager) string {
	if up := o.Above(m.ParentID); up != Owner {
		if host, rest, remote := SplitRemote(up); remote {
			return "your chief " + rest + " on " + host
		}
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
