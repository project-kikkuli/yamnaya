package gh

// checkContext is one row of a commit's check rollup: a check run or a status context.
type checkContext struct {
	Name       string `json:"name"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	CheckSuite struct {
		App *struct {
			Slug string `json:"slug"`
		} `json:"app"`
		WorkflowRun *struct {
			Workflow struct {
				Name string `json:"name"`
			} `json:"workflow"`
		} `json:"workflowRun"`
	} `json:"checkSuite"`
	Context string `json:"context"`
	State   string `json:"state"`
}

// job names the row across commits and reads its result: run ids change per commit and one job name repeats across workflows.
func (c checkContext) job() (name, res string) {
	if c.Context != "" {
		return c.Context, result(c.State)
	}
	name = c.Name
	switch {
	case c.CheckSuite.WorkflowRun != nil:
		name = c.CheckSuite.WorkflowRun.Workflow.Name + ": " + c.Name
	case c.CheckSuite.App != nil:
		name = c.CheckSuite.App.Slug + ": " + c.Name
	}
	if c.Conclusion == "" {
		return name, result(c.Status)
	}
	return name, result(c.Conclusion)
}

// evidence reports whether the row says how a job stands on its commit: a run that stopped short or never ran says nothing.
func (c checkContext) evidence() bool {
	switch c.Conclusion {
	case "SKIPPED", "NEUTRAL", "CANCELLED", "STALE", "ACTION_REQUIRED":
		return false
	}
	return true
}
