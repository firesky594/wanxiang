package planning

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"wanxiang-agent/server/internal/providers"
	"wanxiang-agent/server/internal/tasks"
)

const planSchemaInstruction = `Return one JSON object only with this shape:
{"summary":"string","requires_project_lead":false,"work_items":[{"key":"lowercase-id","title":"string","description":"string","kind":"backend|frontend|qa|docs|security|deployment","required_capabilities":["string"],"acceptance_criteria":["observable result"],"depends_on":["work-item-key"]}]}
Every work item needs at least one acceptance_criteria entry. depends_on may only reference keys in work_items. Do not include markdown fences, credentials, or extra fields.`

func BuildMessages(managerDir string, task tasks.Task) ([]providers.Message, error) {
	content, err := os.ReadFile(filepath.Join(managerDir, "system_prompt.md"))
	if err != nil {
		return nil, fmt.Errorf("read manager system prompt: %w", err)
	}
	system := strings.TrimSpace(string(content)) + "\n\n" + planSchemaInstruction
	user := fmt.Sprintf("Task ID: %d\nTitle: %s\nDescription: %s\nCurrent status: %s\nCreate the smallest independently verifiable work items and their dependencies.", task.ID, task.Title, task.Description, task.Status)
	return []providers.Message{{Role: "system", Content: system}, {Role: "user", Content: user}}, nil
}
