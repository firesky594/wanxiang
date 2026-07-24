package planning

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"wanxiang-agent/server/internal/providers"
	"wanxiang-agent/server/internal/tasks"
)

const (
	maxManagerSystemPromptBytes = 64 * 1024
	maxManagerMemoryBytes       = 32 * 1024
	maxAgentDefinitionBytes     = 64 * 1024
	maxAgentInventoryBytes      = 32 * 1024
)

const planSchemaInstruction = `Return one JSON object only with this shape:
{"summary":"string","requires_project_lead":false,"work_items":[{"key":"lowercase-id","title":"string","description":"string","kind":"backend|frontend|qa|docs|security|deployment","required_capabilities":["string"],"required_skills":["string"],"required_mcps":["string"],"acceptance_criteria":["observable result"],"depends_on":["work-item-key"]}]}
Every work item needs at least one acceptance_criteria entry. depends_on may only reference keys in work_items.
Use exact existing capability identifiers whenever possible. required_skills and required_mcps may only contain exact identifiers present in the Agent inventory below; never invent or imply an installed Skill or MCP. If none is installed, return an empty array and state the missing requirement in the summary. Do not include markdown fences, credentials, or extra fields.`

// BuildMessages 组装 Manager 任务规划模型消息。
func BuildMessages(managerDir string, task tasks.Task) ([]providers.Message, error) {
	content, err := readRegularFile(filepath.Join(managerDir, "system_prompt.md"), maxManagerSystemPromptBytes)
	if err != nil {
		return nil, fmt.Errorf("read manager system prompt: %w", err)
	}
	memory, err := readManagerMemory(managerDir)
	if err != nil {
		return nil, fmt.Errorf("read manager memory: %w", err)
	}
	agentRoot := managerDir
	if filepath.Base(filepath.Clean(managerDir)) == "manager" {
		agentRoot = filepath.Dir(managerDir)
	}
	inventory, err := buildAgentInventory(agentRoot)
	if err != nil {
		return nil, fmt.Errorf("build agent inventory: %w", err)
	}
	system := strings.TrimSpace(string(content))
	if memory != "" {
		system += "\n\n以下是总管持久 Memory，仅用于延续治理决策，不得从中输出凭据：\n" + memory
	}
	system += "\n\n当前 Agent 能力库存（未列出的 Skill/MCP 视为未安装）：\n" + inventory
	system += "\n\n" + planSchemaInstruction
	user := fmt.Sprintf("Task ID: %d\nTitle: %s\nDescription: %s\nCurrent status: %s\nCreate the smallest independently verifiable work items and their dependencies.", task.ID, task.Title, task.Description, task.Status)
	return []providers.Message{{Role: "system", Content: system}, {Role: "user", Content: user}}, nil
}

type promptAgentInventory struct {
	name, role    string
	capabilities  []string
	projectAccess []string
	skills        []string
	mcps          []string
}

func readManagerMemory(managerDir string) (string, error) {
	root := filepath.Join(managerDir, "memory")
	info, err := os.Lstat(root)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("memory root is not a safe directory")
	}
	paths := []string{}
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		if strings.HasPrefix(entry.Name(), ".") {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("memory path contains symlink: %s", entry.Name())
		}
		if !entry.IsDir() {
			paths = append(paths, path)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Strings(paths)
	var result strings.Builder
	for _, path := range paths {
		relative, _ := filepath.Rel(root, path)
		header := "\n### " + filepath.ToSlash(relative) + "\n"
		remaining := maxManagerMemoryBytes - result.Len() - len(header)
		if remaining <= 0 {
			break
		}
		content, err := readRegularFile(path, int64(remaining))
		if err != nil {
			if info, statErr := os.Lstat(path); statErr == nil && info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0 && info.Size() > int64(remaining) {
				content, err = readFilePrefix(path, remaining)
			}
		}
		if err != nil {
			return "", err
		}
		result.WriteString(header)
		result.WriteString(redactMemory(string(content)))
		if result.Len() >= maxManagerMemoryBytes {
			break
		}
	}
	return truncateUTF8(strings.TrimSpace(result.String()), maxManagerMemoryBytes), nil
}

func buildAgentInventory(agentRoot string) (string, error) {
	entries, err := os.ReadDir(agentRoot)
	if os.IsNotExist(err) {
		return "- 当前没有可用 Agent 定义", nil
	}
	if err != nil {
		return "", err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	var result strings.Builder
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		dir := filepath.Join(agentRoot, entry.Name())
		item, err := loadPromptAgentInventory(dir, entry.Name())
		if err != nil {
			continue
		}
		line := fmt.Sprintf("- name=%s; role=%s; capabilities=%s; skills=%s; mcps=%s; project_access=%s\n",
			safeInventoryValue(item.name), safeInventoryValue(item.role), formatInventoryList(item.capabilities),
			formatInventoryList(item.skills), formatInventoryList(item.mcps), formatInventoryList(item.projectAccess))
		if result.Len()+len(line) > maxAgentInventoryBytes {
			result.WriteString("- [Agent 库存已按字节上限截断]\n")
			break
		}
		result.WriteString(line)
	}
	if result.Len() == 0 {
		return "- 当前没有可用 Agent 定义", nil
	}
	return strings.TrimSpace(result.String()), nil
}

func loadPromptAgentInventory(dir, name string) (promptAgentInventory, error) {
	content, err := readRegularFile(filepath.Join(dir, "agent.yaml"), maxAgentDefinitionBytes)
	if err != nil {
		return promptAgentInventory{}, err
	}
	item := promptAgentInventory{name: name}
	current := ""
	for _, line := range strings.Split(string(content), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if len(line) == len(strings.TrimLeft(line, " \t")) {
			parts := strings.SplitN(trimmed, ":", 2)
			if len(parts) != 2 {
				current = ""
				continue
			}
			current = strings.TrimSpace(parts[0])
			if current == "role" {
				item.role = strings.TrimSpace(parts[1])
			}
			continue
		}
		if !strings.HasPrefix(trimmed, "- ") {
			continue
		}
		value := strings.TrimSpace(strings.TrimPrefix(trimmed, "- "))
		switch current {
		case "capabilities":
			item.capabilities = append(item.capabilities, value)
		case "project_access":
			item.projectAccess = append(item.projectAccess, value)
		}
	}
	item.skills, err = listInventoryResources(filepath.Join(dir, "skills"))
	if err != nil {
		return promptAgentInventory{}, err
	}
	item.mcps, err = listInventoryResources(filepath.Join(dir, "mcps"))
	if err != nil {
		return promptAgentInventory{}, err
	}
	return item, nil
}

func listInventoryResources(dir string) ([]string, error) {
	info, statErr := os.Lstat(dir)
	if os.IsNotExist(statErr) {
		return []string{}, nil
	}
	if statErr != nil {
		return nil, statErr
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("%s is not a safe resource directory", filepath.Base(dir))
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	result := []string{}
	for _, entry := range entries {
		if entry.Type()&os.ModeSymlink != 0 || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		result = append(result, strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name())))
	}
	sort.Strings(result)
	return result, nil
}

func readRegularFile(path string, limit int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("%s is not a regular file", filepath.Base(path))
	}
	if info.Size() > limit {
		return nil, fmt.Errorf("%s exceeds %d byte limit", filepath.Base(path), limit)
	}
	return os.ReadFile(path)
}

func readFilePrefix(path string, limit int) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, int64(limit)))
	if err != nil {
		return nil, err
	}
	for len(content) > 0 && !utf8.Valid(content) {
		content = content[:len(content)-1]
	}
	return content, nil
}

func redactMemory(content string) string {
	lines := strings.Split(content, "\n")
	for index, line := range lines {
		lower := strings.ToLower(line)
		for _, marker := range []string{"api_key", "api-key", "apikey", "authorization", "bearer ", "token=", "password", "secret", "cookie", "sk-"} {
			if strings.Contains(lower, marker) {
				lines[index] = "[已脱敏]"
				break
			}
		}
	}
	return strings.Join(lines, "\n")
}

func formatInventoryList(values []string) string {
	if len(values) == 0 {
		return "[]"
	}
	safe := make([]string, 0, len(values))
	for _, value := range values {
		if cleaned := safeInventoryValue(value); cleaned != "" {
			safe = append(safe, cleaned)
		}
	}
	sort.Strings(safe)
	return "[" + strings.Join(safe, ",") + "]"
}

func safeInventoryValue(value string) string {
	value = strings.TrimSpace(strings.NewReplacer("\r", "", "\n", "", ";", ",", "`", "").Replace(value))
	runes := []rune(value)
	if len(runes) > 120 {
		value = string(runes[:120])
	}
	lower := strings.ToLower(value)
	for _, marker := range []string{"api_key", "api-key", "authorization", "bearer ", "token=", "password", "secret", "cookie", "sk-"} {
		if strings.Contains(lower, marker) {
			return "[已脱敏]"
		}
	}
	return value
}

func truncateUTF8(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	content := []byte(value)[:limit]
	for len(content) > 0 && !utf8.Valid(content) {
		content = content[:len(content)-1]
	}
	return string(content)
}
