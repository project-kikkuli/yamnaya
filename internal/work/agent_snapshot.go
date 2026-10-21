package work

type AgentSnapshot struct {
	Messages    []AgentMessage
	Agent       Agent
	Workspace   Scope
	Work        []Scope
	Pending     map[string]int64
	Attachments []Attachment
	Rules       []Rule
	Errors      map[string]string
}
