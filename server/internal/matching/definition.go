package matching

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"wanxiang-agent/server/internal/files"
)

type AgentDefinition struct {
	Name           string
	Role           string
	Capabilities   []string
	MaxConcurrency int
	ProjectAccess  []string
	Skills         []string
	MCPs           []string
	MemorySummary  string
}

var definitionNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

func LoadDefinition(agentRoot, name string) (AgentDefinition, error) {
	if !definitionNamePattern.MatchString(name) {
		return AgentDefinition{}, errors.New("invalid agent name")
	}
	dir, err := files.UnderRoot(agentRoot, filepath.Join(agentRoot, name))
	if err != nil {
		return AgentDefinition{}, err
	}
	content, err := os.ReadFile(filepath.Join(dir, "agent.yaml"))
	if err != nil {
		return AgentDefinition{}, fmt.Errorf("read agent definition: %w", err)
	}
	result := AgentDefinition{Name: name, MaxConcurrency: 1}
	current := ""
	for number, line := range strings.Split(string(content), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if len(line) == len(strings.TrimLeft(line, " \t")) {
			parts := strings.SplitN(trimmed, ":", 2)
			if len(parts) != 2 {
				return AgentDefinition{}, fmt.Errorf("invalid agent definition line %d", number+1)
			}
			key, value := strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
			current = key
			switch key {
			case "api_key", "secret", "token", "password":
				return AgentDefinition{}, fmt.Errorf("secret field %q is forbidden", key)
			case "role":
				result.Role = value
			case "max_concurrency":
				parsed, e := strconv.Atoi(value)
				if e != nil || parsed < 1 {
					return AgentDefinition{}, errors.New("max_concurrency must be positive")
				}
				result.MaxConcurrency = parsed
			case "capabilities", "project_access":
				if value != "" {
					return AgentDefinition{}, fmt.Errorf("%s must be a list", key)
				}
			}
			continue
		}
		if strings.HasPrefix(trimmed, "- ") {
			value := strings.TrimSpace(strings.TrimPrefix(trimmed, "- "))
			switch current {
			case "capabilities":
				result.Capabilities = append(result.Capabilities, value)
			case "project_access":
				result.ProjectAccess = append(result.ProjectAccess, value)
			}
		}
	}
	if result.Role == "" {
		return AgentDefinition{}, errors.New("agent role is required")
	}
	result.Skills, err = listResourceNames(filepath.Join(dir, "skills"))
	if err != nil {
		return AgentDefinition{}, err
	}
	result.MCPs, err = listResourceNames(filepath.Join(dir, "mcps"))
	if err != nil {
		return AgentDefinition{}, err
	}
	result.MemorySummary, err = readSummaries(filepath.Join(dir, "memory", "summaries"))
	if err != nil {
		return AgentDefinition{}, err
	}
	return result, nil
}

func listResourceNames(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return []string{}, nil
	}
	if err != nil {
		return nil, err
	}
	result := []string{}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), ".") {
			result = append(result, strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name())))
		}
	}
	sort.Strings(result)
	return result, nil
}

func readSummaries(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	var result strings.Builder
	for _, entry := range entries {
		if entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		content, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			return "", err
		}
		remaining := 16384 - result.Len()
		if remaining <= 0 {
			break
		}
		if len(content) > remaining {
			content = content[:remaining]
		}
		result.Write(content)
		result.WriteByte('\n')
	}
	return strings.TrimSpace(result.String()), nil
}
