package cli

import (
	"encoding/json"

	"github.com/project-kikkuli/yamnaya/internal/work"
)

func checkpointSchema(x work.AgentSnapshot) []byte {
	text := map[string]any{"type": "string"}
	object := func(properties map[string]any) map[string]any {
		required := make([]string, 0, len(properties))
		for name := range properties {
			required = append(required, name)
		}
		return map[string]any{"type": "object", "properties": properties, "required": required, "additionalProperties": false}
	}
	inputs := map[string]any{}
	for id := range x.Pending {
		inputs[id] = map[string]any{"type": "integer"}
	}
	schema := object(map[string]any{
		"Brief":  object(map[string]any{"Assessment": text, "Rationale": text, "Changed": text, "Uncertainty": text, "Context": map[string]any{"type": "integer"}}),
		"Inputs": object(inputs), "Next": text, "WaitFor": text, "WakeAt": map[string]any{"type": []string{"string", "null"}},
	})
	raw, _ := json.Marshal(schema)
	return raw
}
