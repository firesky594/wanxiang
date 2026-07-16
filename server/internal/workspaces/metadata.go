package workspaces

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

type ProjectAgent struct {
	Name      string
	ReportsTo string
}
type ProjectMetadata struct {
	MetadataVersion int
	Project         string
	Manager         string
	ProjectLead     string
	Agents          []ProjectAgent
	BranchPolicy    string
	MergeTarget     string
}
type AssignmentMetadata struct {
	MetadataVersion int
	TaskID          int64
	StepID          int64
	AssignmentID    int64
	WorkItemKey     string
	AgentName       string
	ReportsTo       string
	BranchName      string
	WorktreeID      string
	BaseCommit      string
	WriteScope      []string
	Status          string
}

var safeName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)
var safeKey = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

func EncodeProject(value ProjectMetadata) ([]byte, error) {
	if value.MetadataVersion != 1 || !safeName.MatchString(value.Project) || !safeName.MatchString(value.Manager) {
		return nil, errors.New("invalid project metadata")
	}
	if value.ProjectLead != "" && !safeName.MatchString(value.ProjectLead) {
		return nil, errors.New("invalid project lead")
	}
	if value.BranchPolicy == "" || value.MergeTarget != "main" {
		return nil, errors.New("invalid branch policy")
	}
	var out strings.Builder
	fmt.Fprintf(&out, "metadata_version: %d\nproject: %s\nmanager: %s\nproject_lead: %s\nagents:\n", value.MetadataVersion, quote(value.Project), quote(value.Manager), quote(value.ProjectLead))
	for _, agent := range value.Agents {
		if !safeName.MatchString(agent.Name) || (agent.ReportsTo != "" && !safeName.MatchString(agent.ReportsTo)) {
			return nil, errors.New("invalid project agent")
		}
		fmt.Fprintf(&out, "  - name: %s\n    reports_to: %s\n", quote(agent.Name), quote(agent.ReportsTo))
	}
	fmt.Fprintf(&out, "branch_policy: %s\nmerge_target: %s\n", quote(value.BranchPolicy), quote(value.MergeTarget))
	return []byte(out.String()), nil
}

func EncodeAssignment(value AssignmentMetadata) ([]byte, string, error) {
	if err := validateAssignment(value); err != nil {
		return nil, "", err
	}
	var out strings.Builder
	fmt.Fprintf(&out, "metadata_version: %d\ntask_id: %d\nstep_id: %d\nassignment_id: %d\nwork_item_key: %s\nagent_name: %s\nreports_to: %s\nbranch_name: %s\nworktree_id: %s\nbase_commit: %s\nwrite_scope:\n", value.MetadataVersion, value.TaskID, value.StepID, value.AssignmentID, quote(value.WorkItemKey), quote(value.AgentName), quote(value.ReportsTo), quote(value.BranchName), quote(value.WorktreeID), quote(value.BaseCommit))
	for _, scope := range value.WriteScope {
		fmt.Fprintf(&out, "  - %s\n", quote(scope))
	}
	fmt.Fprintf(&out, "status: %s\n", quote(value.Status))
	encoded := []byte(out.String())
	sum := sha256.Sum256(encoded)
	return encoded, hex.EncodeToString(sum[:]), nil
}

func DecodeAssignment(content []byte) (AssignmentMetadata, error) {
	var result AssignmentMetadata
	seen := map[string]bool{}
	section := ""
	for number, line := range strings.Split(string(content), "\n") {
		if line == "" {
			continue
		}
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(line, "  - ") {
			if section != "write_scope" {
				return result, fmt.Errorf("unexpected list at line %d", number+1)
			}
			value, err := unquote(strings.TrimSpace(strings.TrimPrefix(line, "  - ")))
			if err != nil {
				return result, err
			}
			result.WriteScope = append(result.WriteScope, value)
			continue
		}
		if len(line) != len(strings.TrimLeft(line, " \t")) {
			return result, fmt.Errorf("unexpected indentation at line %d", number+1)
		}
		parts := strings.SplitN(trimmed, ":", 2)
		if len(parts) != 2 {
			return result, fmt.Errorf("invalid line %d", number+1)
		}
		key, raw := parts[0], strings.TrimSpace(parts[1])
		if seen[key] {
			return result, fmt.Errorf("duplicate field %s", key)
		}
		seen[key] = true
		section = key
		setString := func(target *string) error {
			parsed, err := unquote(raw)
			if err != nil {
				return err
			}
			*target = parsed
			return nil
		}
		switch key {
		case "metadata_version":
			parsed, err := strconv.Atoi(raw)
			if err != nil {
				return result, err
			}
			result.MetadataVersion = parsed
		case "task_id":
			parsed, err := strconv.ParseInt(raw, 10, 64)
			if err != nil {
				return result, err
			}
			result.TaskID = parsed
		case "step_id":
			parsed, err := strconv.ParseInt(raw, 10, 64)
			if err != nil {
				return result, err
			}
			result.StepID = parsed
		case "assignment_id":
			parsed, err := strconv.ParseInt(raw, 10, 64)
			if err != nil {
				return result, err
			}
			result.AssignmentID = parsed
		case "work_item_key":
			if err := setString(&result.WorkItemKey); err != nil {
				return result, err
			}
		case "agent_name":
			if err := setString(&result.AgentName); err != nil {
				return result, err
			}
		case "reports_to":
			if err := setString(&result.ReportsTo); err != nil {
				return result, err
			}
		case "branch_name":
			if err := setString(&result.BranchName); err != nil {
				return result, err
			}
		case "worktree_id":
			if err := setString(&result.WorktreeID); err != nil {
				return result, err
			}
		case "base_commit":
			if err := setString(&result.BaseCommit); err != nil {
				return result, err
			}
		case "write_scope":
			if raw != "" {
				return result, errors.New("write_scope must be a list")
			}
		case "status":
			if err := setString(&result.Status); err != nil {
				return result, err
			}
		default:
			return result, fmt.Errorf("unknown field %s", key)
		}
	}
	if err := validateAssignment(result); err != nil {
		return AssignmentMetadata{}, err
	}
	return result, nil
}

func validateAssignment(value AssignmentMetadata) error {
	if value.MetadataVersion != 1 || value.TaskID < 1 || value.StepID < 1 || value.AssignmentID < 1 {
		return errors.New("invalid assignment identifiers")
	}
	if !safeKey.MatchString(value.WorkItemKey) || !safeName.MatchString(value.AgentName) {
		return errors.New("invalid assignment name")
	}
	if value.ReportsTo != "" && !safeName.MatchString(value.ReportsTo) {
		return errors.New("invalid reports_to")
	}
	want := fmt.Sprintf("agent/%s/%d-%d-%s", value.AgentName, value.TaskID, value.StepID, value.WorkItemKey)
	if value.BranchName != want {
		return errors.New("invalid assignment branch")
	}
	if value.WorktreeID == "" || value.BaseCommit == "" || value.Status == "" || len(value.WriteScope) == 0 {
		return errors.New("incomplete assignment metadata")
	}
	for _, scope := range value.WriteScope {
		clean := filepath.Clean(scope)
		if scope == "" || filepath.IsAbs(scope) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) || strings.Contains(scope, "\\") {
			return errors.New("unsafe write scope")
		}
	}
	return nil
}
func quote(value string) string            { return strconv.Quote(value) }
func unquote(value string) (string, error) { return strconv.Unquote(value) }
