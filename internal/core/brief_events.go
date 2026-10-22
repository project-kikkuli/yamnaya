package core

import "fmt"

// meaningfulEvents lists, by manager id, the goal events a brief must not predate: today a verification that failed or could not finish.
func (c *Core) meaningfulEvents() (map[string][]stamped, error) {
	goals, err := c.Store.Goals()
	if err != nil {
		return nil, err
	}
	managerOf := map[string]string{}
	for _, g := range goals {
		managerOf[g.ID] = g.ManagerID
	}
	all, err := c.Store.Verifications("")
	if err != nil {
		return nil, err
	}
	out := map[string][]stamped{}
	for _, v := range all {
		if v.DecidedAt.IsZero() || v.Verdict == VerdictSuperseded || v.Verdict == VerdictPass {
			continue
		}
		m := managerOf[v.Goal]
		out[m] = append(out[m], stamped{At: v.DecidedAt, What: fmt.Sprintf("verification %d: %s", v.Attempt, v.Verdict)})
	}
	return out, nil
}
