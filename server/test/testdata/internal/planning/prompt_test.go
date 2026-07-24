package planning

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"wanxiang-agent/server/internal/tasks"
)

func TestBuildMessagesIncludesRulesTaskAndSchema(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "system_prompt.md"), []byte("manager rules"), 0o644); err != nil {
		t.Fatal(err)
	}
	messages, err := BuildMessages(dir, tasks.Task{ID: 7, Title: "Build auth", Description: "No secrets", Status: "created"})
	if err != nil {
		t.Fatal(err)
	}
	joined := messages[0].Content + messages[1].Content
	for _, want := range []string{"manager rules", "Build auth", "acceptance_criteria", "depends_on"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("messages=%+v missing=%q", messages, want)
		}
	}
	if messages[0].Role != "system" || messages[1].Role != "user" {
		t.Fatalf("messages=%+v", messages)
	}
}
