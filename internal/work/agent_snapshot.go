package work

type AgentSnapshot struct {
	Agent       Agent
	Workspace   Scope
	Work        []Scope
	Pending     map[string]int64
	Attachments []Attachment
	Rules       []Rule
	Errors      map[string]string
}
