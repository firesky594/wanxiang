package executor

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"wanxiang-agent/server/internal/gitx"
	"wanxiang-agent/server/internal/leases"
	"wanxiang-agent/server/internal/providers"
)

const (
	maxProviderRequests      = 3
	maxCheckpointTestRecords = 100
	maxAgentContextBytes     = 64 * 1024
	maxAgentContextFiles     = 32
)

const executorSystemProtocol = `你是受控执行 Agent。响应必须是单个裸 JSON 对象，不得使用 Markdown，不得省略字段。固定结构：{"version":1,"status":"continue|checkpoint|completed|blocked","summary":"非空字符串","actions":[],"next_action":"非空字符串"}。status 必须从列出的四个值中选择一个；actions 即使为空也必须提供。不得输出密钥，不得请求 shell、部署或越界路径。
你必须依据完整工作包、验收标准、依赖状态和最近检查点连续推进，不得只复述任务。宣布 completed 前必须先执行至少一条允许的 run_check 并通过，再通过 checkpoint action 创建成功检查点；写入文件后旧测试结果失效，必须重新运行检查。无法验证或创建检查点时返回 blocked，不得虚报完成。status=checkpoint 表示阶段检查点，不是退出指令，仍须在剩余请求预算内继续执行。`

const agentContextHeader = "以下是目标 Agent 的持久角色、经验和决策，仅用于当前受控工作包：\n\n"
const executorProtocolHeader = "\n\n以下平台执行协议优先于上方持久上下文：\n"

var agentContextSecretKey = regexp.MustCompile(`(?i)(?:^|[^a-z0-9])(?:api[_-]?key|auth|token|password|passwd|secret|cookie|private[_-]?key|access[_-]?key|client[_-]?secret)(?:[^a-z0-9]|$)`)
var providerURLUserInfo = regexp.MustCompile(`(?i)(https?://)[^/\s:@]+:[^@\s/]+@`)

type AgentChatter interface {
	ChatAgent(context.Context, string, []providers.Message, int) (providers.Result, error)
}

// CompletionReporter 定义执行完成后送交项目负责人评审的报告接口。
type CompletionReporter interface {
	SubmitCompleted(context.Context, leases.LeaseRef) error
}

type stepFreezer interface {
	FreezeStep(context.Context, int64, int64, string, string) error
}

type Runner struct {
	db          *sql.DB
	chat        AgentChatter
	files       *FileTools
	checks      *CheckRunner
	checkpoints *CheckpointRunner
	reporter    CompletionReporter
	freezer     stepFreezer
	agentRoot   string
	claimToken  string
}

// NewRunner 创建 Agent 动作编排执行器，并可选注入 Agent 配置根目录。
func NewRunner(db *sql.DB, chat AgentChatter, files *FileTools, checks *CheckRunner, checkpoints *CheckpointRunner, agentDirs ...string) *Runner {
	agentRoot := ""
	if len(agentDirs) > 0 {
		agentRoot = agentDirs[0]
	}
	return &Runner{db: db, chat: chat, files: files, checks: checks, checkpoints: checkpoints, agentRoot: agentRoot}
}

// SetCompletionReporter 注入完成报告提交器；未注入时保持旧调用方的执行行为。
func (r *Runner) SetCompletionReporter(reporter CompletionReporter) *Runner {
	r.reporter = reporter
	return r
}

// Run 循环执行受控动作，并仅在干净检查点和完成报告送审成功后完成。
func (r *Runner) Run(ctx context.Context, input WorkerInput) (WorkerResult, error) {
	result := WorkerResult{Status: RunRunning}
	ref := leases.LeaseRef{TaskID: input.TaskID, StepID: input.StepID, AgentName: input.AgentName, LeaseID: input.LeaseID, LeaseVersion: input.LeaseVersion}
	r.claimToken = input.ClaimToken
	runID, err := r.startRun(ctx, ref)
	if err != nil {
		result.Status = RunFailed
		return result, err
	}
	messages := []providers.Message{{Role: "system", Content: r.systemPrompt(input.AgentName)}, {Role: "user", Content: r.workPrompt(ctx, input)}}
	sequence := r.lastActionSequence(ctx, runID)
	checkpointReached := r.hasLeaseCheckpoint(ctx, ref)
	var checkEvidence []leases.CheckpointTest
	for request := 1; request <= maxProviderRequests; request++ {
		if err := r.requireExecutionClaim(ctx, runID); err != nil {
			result.Status = RunStopped
			return r.finish(ctx, runID, result, err)
		}
		chatResult, chatErr := r.chat.ChatAgent(ctx, input.AgentName, messages, 2048)
		result.RequestCount = request
		if chatErr != nil {
			if retryableProviderError(chatErr) && request < maxProviderRequests {
				continue
			}
			result.Status = providerFailureStatus(chatErr)
			return r.fail(ctx, runID, ref, result, chatErr, "executor_provider_failure")
		}
		result.InputTokens += chatResult.InputTokens
		result.OutputTokens += chatResult.OutputTokens
		response, parseErr := ParseProviderResponse(chatResult.Content)
		if parseErr != nil {
			result.Status = RunFailed
			return r.fail(ctx, runID, ref, result, parseErr, "executor_protocol_failure")
		}
		messages = append(messages, providers.Message{Role: "assistant", Content: chatResult.Content})
		for _, action := range response.Actions {
			sequence++
			actionResult, actionErr := r.executeAction(ctx, ref, response, action, &checkEvidence)
			actionResult = redactProviderToolResult(actionResult)
			r.auditAction(ctx, runID, sequence, action, actionResult, actionErr)
			if actionErr != nil {
				result.Status = RunFailed
				return r.fail(ctx, runID, ref, result, actionErr, "executor_action_failure")
			}
			switch action.Type {
			case ActionCheckpoint:
				checkpointReached = true
			case ActionWriteFile:
				checkpointReached = false
			}
			messages = append(messages, providers.Message{Role: "user", Content: "动作结果：" + actionResult})
		}
		result.Summary = response.Summary
		result.NextAction = response.NextAction
		switch response.Status {
		case ProviderCompleted:
			if (r.checkpoints != nil || r.reporter != nil) && !checkpointReached {
				result.Status = RunFailed
				return r.fail(ctx, runID, ref, result, errors.New("provider completed without a valid checkpoint"), "executor_checkpoint_missing")
			}
			if r.reporter != nil {
				if reportErr := r.reporter.SubmitCompleted(ctx, ref); reportErr != nil {
					result.Status = RunFailed
					return r.fail(ctx, runID, ref, result, fmt.Errorf("submit completion report: %w", reportErr), "executor_completion_blocked")
				}
			}
			result.Status = RunCompleted
			return r.finish(ctx, runID, result, nil)
		case ProviderBlocked:
			result.Status = RunStopped
			return r.fail(ctx, runID, ref, result, errors.New("provider reported blocked"), "executor_provider_blocked")
		case ProviderCheckpoint:
			if !checkpointReached && request == maxProviderRequests {
				result.Status = RunStopped
				return r.fail(ctx, runID, ref, result, errors.New("provider reported checkpoint without creating one"), "executor_checkpoint_missing")
			}
			if request < maxProviderRequests {
				messages = append(messages, providers.Message{Role: "user", Content: "阶段检查点已收到。不要退出；根据上方动作结果和 next_action 继续执行剩余工作。若尚未调用 checkpoint action，必须先创建真实检查点，再继续直到满足验收并返回 completed，或给出明确 blocked 原因。"})
				continue
			}
			result.Status = RunCheckpointed
			return r.finish(ctx, runID, result, nil)
		}
	}
	if checkpointReached {
		result.Status = RunCheckpointed
		return r.finish(ctx, runID, result, nil)
	}
	result.Status = RunFailed
	return r.fail(ctx, runID, ref, result, errors.New("provider request budget exhausted"), "executor_request_budget_exhausted")
}

func (r *Runner) systemPrompt(agentName string) string {
	contextText := r.loadAgentContext(agentName)
	if contextText == "" {
		return executorSystemProtocol
	}
	return agentContextHeader + contextText + executorProtocolHeader + executorSystemProtocol
}

func (r *Runner) loadAgentContext(agentName string) string {
	agentDir, err := safeAgentDirectory(r.agentRoot, agentName)
	if err != nil {
		return ""
	}
	remaining := maxAgentContextBytes - len(agentContextHeader) - len(executorProtocolHeader) - len(executorSystemProtocol)
	if remaining <= 0 {
		return ""
	}
	loaded := 0
	var content strings.Builder
	if appendAgentContextFile(&content, &remaining, agentDir, filepath.Join(agentDir, "system_prompt.md"), "system_prompt.md") {
		loaded++
	}
	for _, memoryDir := range []string{"summaries", "decisions"} {
		if remaining == 0 || loaded >= maxAgentContextFiles {
			break
		}
		root := filepath.Join(agentDir, "memory", memoryDir)
		for _, path := range agentContextFiles(root) {
			if remaining == 0 || loaded >= maxAgentContextFiles {
				break
			}
			relative, err := filepath.Rel(agentDir, path)
			if err != nil {
				continue
			}
			if appendAgentContextFile(&content, &remaining, agentDir, path, filepath.ToSlash(relative)) {
				loaded++
			}
		}
	}
	return strings.TrimSpace(content.String())
}

func safeAgentDirectory(root, agentName string) (string, error) {
	if strings.TrimSpace(root) == "" || strings.TrimSpace(agentName) != agentName || agentName == "" ||
		agentName == "." || agentName == ".." || filepath.IsAbs(agentName) ||
		filepath.Base(agentName) != agentName || strings.ContainsAny(agentName, `/\`) {
		return "", errors.New("invalid agent context path")
	}
	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	resolvedRoot, err := filepath.EvalSymlinks(absoluteRoot)
	if err != nil {
		return "", err
	}
	agentDir := filepath.Join(resolvedRoot, agentName)
	info, err := os.Lstat(agentDir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("invalid agent context directory")
	}
	resolvedAgent, err := filepath.EvalSymlinks(agentDir)
	if err != nil || filepath.Clean(resolvedAgent) != filepath.Clean(agentDir) || !pathWithinRoot(resolvedRoot, resolvedAgent) {
		return "", errors.New("agent context escapes root")
	}
	return resolvedAgent, nil
}

func agentContextFiles(root string) []string {
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil
	}
	paths := make([]string, 0)
	_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if entry != nil && entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if path == root {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		name := strings.ToLower(entry.Name())
		if !entry.Type().IsRegular() || name == "env" || strings.HasPrefix(name, ".env") || strings.HasSuffix(name, ".log") {
			return nil
		}
		paths = append(paths, path)
		if len(paths) >= maxAgentContextFiles {
			return fs.SkipAll
		}
		return nil
	})
	sort.Strings(paths)
	return paths
}

func appendAgentContextFile(builder *strings.Builder, remaining *int, agentDir, path, label string) bool {
	if *remaining <= 0 {
		return false
	}
	value, truncated, err := readAgentContextFile(agentDir, path, *remaining)
	if err != nil {
		return false
	}
	section := "### " + label + "\n" + strings.TrimSpace(value)
	if truncated {
		section += "\n[内容已按上下文上限截断]"
	}
	if builder.Len() > 0 {
		section = "\n\n" + section
	}
	appendBoundedContext(builder, remaining, section)
	return true
}

func readAgentContextFile(agentDir, path string, limit int) (string, bool, error) {
	if limit <= 0 || !pathWithinRoot(agentDir, path) {
		return "", false, errors.New("invalid context file path")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return "", false, errors.New("context file is not regular")
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || filepath.Clean(resolved) != filepath.Clean(path) || !pathWithinRoot(agentDir, resolved) {
		return "", false, errors.New("context file escapes agent directory")
	}
	file, err := os.Open(path)
	if err != nil {
		return "", false, err
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, int64(limit)+1))
	if err != nil {
		return "", false, err
	}
	value := redactAgentContext(strings.ToValidUTF8(string(raw), ""))
	value, shortened := truncateUTF8(value, limit)
	return value, len(raw) > limit || shortened, nil
}

func redactAgentContext(value string) string {
	lines := strings.Split(value, "\n")
	redactIndentedBelow := -1
	redactContinuation := false
	redactNextValue := false
	redactStructuredDepth := 0
	secretFence := ""
	privateKeyBlock := false
	for index, line := range lines {
		trimmed := strings.TrimSpace(line)
		lower := strings.ToLower(trimmed)
		if privateKeyBlock {
			lines[index] = "[已脱敏]"
			if strings.Contains(lower, "-----end ") && strings.Contains(lower, "private key-----") {
				privateKeyBlock = false
			}
			continue
		}
		if secretFence != "" {
			lines[index] = "[已脱敏]"
			if strings.Contains(trimmed, secretFence) {
				secretFence = ""
			}
			continue
		}
		if redactStructuredDepth > 0 {
			lines[index] = "[已脱敏]"
			redactStructuredDepth += contextStructuredDelta(trimmed)
			if redactStructuredDepth < 0 {
				redactStructuredDepth = 0
			}
			continue
		}
		if redactContinuation {
			lines[index] = "[已脱敏]"
			redactContinuation = strings.HasSuffix(trimmed, `\`)
			continue
		}
		if redactNextValue && trimmed != "" {
			lines[index] = "[已脱敏]"
			redactNextValue = false
			continue
		}
		indent := leadingContextIndent(line)
		if redactIndentedBelow >= 0 {
			if trimmed == "" {
				continue
			}
			if indent > redactIndentedBelow {
				lines[index] = "[已脱敏]"
				continue
			}
			redactIndentedBelow = -1
		}
		if strings.Contains(lower, "-----begin ") && strings.Contains(lower, "private key-----") {
			lines[index] = "[已脱敏]"
			privateKeyBlock = true
			continue
		}
		if agentContextSecretKey.MatchString(lower) || strings.Contains(lower, "authorization") ||
			strings.Contains(lower, "bearer ") || strings.Contains(lower, "sk-") {
			lines[index] = "[已脱敏]"
			redactIndentedBelow = indent
			redactContinuation = strings.HasSuffix(trimmed, `\`)
			redactNextValue = contextSecretNeedsNextValue(trimmed)
			redactStructuredDepth = contextSecretStructuredDepth(trimmed)
			for _, fence := range []string{`"""`, `'''`} {
				if strings.Count(trimmed, fence)%2 == 1 {
					secretFence = fence
					break
				}
			}
		}
	}
	return strings.Join(lines, "\n")
}

func contextSecretStructuredDepth(value string) int {
	index := strings.IndexAny(value, ":=")
	if index < 0 || index+1 >= len(value) {
		return 0
	}
	depth := contextStructuredDelta(value[index+1:])
	if depth < 0 {
		return 0
	}
	return depth
}

func contextStructuredDelta(value string) int {
	depth := 0
	var quote rune
	escaped := false
	for _, current := range value {
		if escaped {
			escaped = false
			continue
		}
		if quote != 0 {
			if current == '\\' {
				escaped = true
			} else if current == quote {
				quote = 0
			}
			continue
		}
		switch current {
		case '"', '\'':
			quote = current
		case '[', '{':
			depth++
		case ']', '}':
			depth--
		}
	}
	return depth
}

func leadingContextIndent(value string) int {
	return len(value) - len(strings.TrimLeft(value, " \t"))
}

func contextSecretNeedsNextValue(value string) bool {
	value = strings.TrimSpace(value)
	if strings.HasSuffix(value, ":") || strings.HasSuffix(value, "=") {
		return true
	}
	fields := strings.Fields(value)
	if len(fields) == 0 {
		return false
	}
	switch fields[len(fields)-1] {
	case "|", "|-", "|+", ">", ">-", ">+":
		return true
	default:
		return false
	}
}

func appendBoundedContext(builder *strings.Builder, remaining *int, value string) {
	if *remaining <= 0 {
		return
	}
	value, _ = truncateUTF8(value, *remaining)
	builder.WriteString(value)
	*remaining -= len(value)
}

func truncateUTF8(value string, limit int) (string, bool) {
	if limit < 0 {
		limit = 0
	}
	if len(value) <= limit {
		return value, false
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(value[cut]) {
		cut--
	}
	return value[:cut], true
}

func pathWithinRoot(root, target string) bool {
	relative, err := filepath.Rel(root, target)
	return err == nil && relative != ".." && !filepath.IsAbs(relative) &&
		!strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func redactProviderToolResult(value string) string {
	value = providerURLUserInfo.ReplaceAllString(value, `${1}[REDACTED]@`)
	return Redact(redactAgentContext(value))
}

func (r *Runner) executeAction(ctx context.Context, ref leases.LeaseRef, response ProviderResponse, action ActionRequest, tests *[]leases.CheckpointTest) (string, error) {
	switch action.Type {
	case ActionReadFile:
		content, err := r.files.ReadFile(ctx, ref, action.Path)
		return string(content), err
	case ActionWriteFile:
		err := r.files.WriteFile(ctx, ref, action.Path, []byte(action.Content))
		if err == nil {
			*tests = nil
		}
		return "write completed", err
	case ActionRunCheck:
		got := r.checks.RunCheck(ctx, ref, CheckRequest{Command: action.Command, Args: action.Args})
		if got.Error != "" {
			return got.Output, errors.New(got.Error)
		}
		appendCheckpointTest(tests, leases.CheckpointTest{Command: got.Command, Result: "passed"})
		return got.Output, nil
	case ActionGitStatus:
		root, err := r.files.workspaceRoot(ctx, ref)
		if err != nil {
			return "", err
		}
		out, err := gitx.Run(ctx, root, "status", "--short", "--branch")
		return Redact(out), err
	case ActionCheckpoint:
		if r.checkpoints == nil {
			return "", errors.New("checkpoint tool unavailable")
		}
		cp, err := r.checkpoints.CreateGitCheckpoint(ctx, ref, WorkerSummary{Completed: []string{response.Summary}, NextAction: response.NextAction, Tests: append([]leases.CheckpointTest(nil), (*tests)...)})
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("checkpoint %d", cp.ID), nil
	default:
		return "", errors.New("unknown action")
	}
}

func appendCheckpointTest(tests *[]leases.CheckpointTest, evidence leases.CheckpointTest) {
	for index, existing := range *tests {
		if existing.Command == evidence.Command {
			(*tests)[index] = evidence
			return
		}
	}
	*tests = append(*tests, evidence)
	if len(*tests) > maxCheckpointTestRecords {
		*tests = append([]leases.CheckpointTest(nil), (*tests)[len(*tests)-maxCheckpointTestRecords:]...)
	}
}

func (r *Runner) workPrompt(ctx context.Context, input WorkerInput) string {
	var title, description, projectSlug, kind, stepStatus, workPackage string
	err := r.db.QueryRowContext(ctx, `select t.title,t.description,p.slug,ts.kind,ts.status,ts.input
		from tasks t join projects p on p.id=t.project_id join task_steps ts on ts.task_id=t.id
		where t.id=? and ts.id=?`, input.TaskID, input.StepID).
		Scan(&title, &description, &projectSlug, &kind, &stepStatus, &workPackage)
	if err != nil {
		return fmt.Sprintf("任务 %d，步骤 %d。工作包上下文不可用；不要猜测内容，返回 blocked。", input.TaskID, input.StepID)
	}

	var branchName, worktreePath, workspaceStatus, writeScope string
	workspaceErr := r.db.QueryRowContext(ctx, `select branch_name,worktree_path,status,write_scope_json
		from project_workspaces where task_id=? and step_id=? and agent_name=?`,
		input.TaskID, input.StepID, input.AgentName).
		Scan(&branchName, &worktreePath, &workspaceStatus, &writeScope)
	if workspaceErr != nil {
		branchName, worktreePath, workspaceStatus, writeScope = "未登记", "未登记", "missing", "[]"
	}

	dependencyStatus := "无"
	rows, dependencyErr := r.db.QueryContext(ctx, `select dep.id,dep.kind,dep.agent_name,dep.status
		from workflow_edges edge join task_steps dep on dep.id=edge.from_step_id
		where edge.task_id=? and edge.to_step_id=? order by dep.id`, input.TaskID, input.StepID)
	if dependencyErr == nil {
		var dependencies []string
		var scanErr error
		for rows.Next() {
			var stepID int64
			var dependencyKind, dependencyAgent, status string
			if scanErr = rows.Scan(&stepID, &dependencyKind, &dependencyAgent, &status); scanErr != nil {
				break
			}
			dependencies = append(dependencies, fmt.Sprintf("- 步骤 %d / %s / %s：%s", stepID, dependencyKind, dependencyAgent, status))
		}
		if rows.Err() != nil {
			scanErr = rows.Err()
		}
		rows.Close()
		if scanErr != nil {
			dependencyStatus = "读取失败；不得假设依赖已经完成"
		} else if len(dependencies) > 0 {
			dependencyStatus = strings.Join(dependencies, "\n")
		}
	} else {
		dependencyStatus = "读取失败；不得假设依赖已经完成"
	}

	checkpointSummary := "无"
	var checkpointID int64
	var checkpointJSON, checkpointCommit, checkpointCreatedAt string
	checkpointErr := r.db.QueryRowContext(ctx, `select id,summary_json,git_commit,created_at
		from task_checkpoints where task_id=? and step_id=? order by id desc limit 1`,
		input.TaskID, input.StepID).
		Scan(&checkpointID, &checkpointJSON, &checkpointCommit, &checkpointCreatedAt)
	if checkpointErr == nil {
		checkpointSummary = fmt.Sprintf("检查点 %d / commit %s / %s\n%s", checkpointID, checkpointCommit, checkpointCreatedAt, checkpointJSON)
	} else if !errors.Is(checkpointErr, sql.ErrNoRows) {
		checkpointSummary = "读取失败；从当前工作区状态重新核对"
	}

	var prompt strings.Builder
	fmt.Fprintf(&prompt, "任务 ID：%d\n步骤 ID：%d\n目标 Agent：%s\n项目 slug：%s\n任务标题：%s\n任务描述：%s\n步骤类型：%s\n步骤状态：%s\n",
		input.TaskID, input.StepID, input.AgentName, projectSlug, title, description, kind, stepStatus)
	fmt.Fprintf(&prompt, "\n工作区状态：%s\n分支：%s\n工作区：%s\n写入范围：%s\n动作中的 path 必须使用工作区相对路径。\n",
		workspaceStatus, branchName, worktreePath, writeScope)
	prompt.WriteString("\n完整工作包（包含验收标准，必须逐项完成）：\n")
	prompt.WriteString(workPackage)
	prompt.WriteString("\n\n依赖状态：\n")
	prompt.WriteString(dependencyStatus)
	prompt.WriteString("\n\n最近 checkpoint 摘要：\n")
	prompt.WriteString(checkpointSummary)
	prompt.WriteString("\n\n请直接检查和修改工作区并运行允许的校验。每轮根据动作结果继续推进；完成验收前必须创建 checkpoint。")
	return prompt.String()
}

func (r *Runner) hasLeaseCheckpoint(ctx context.Context, ref leases.LeaseRef) bool {
	var count int
	err := r.db.QueryRowContext(ctx, `select count(*) from task_checkpoints
		where task_id=? and step_id=? and lease_id=? and clean=1 and git_commit<>''`,
		ref.TaskID, ref.StepID, ref.LeaseID).Scan(&count)
	return err == nil && count > 0
}

func (r *Runner) startRun(ctx context.Context, ref leases.LeaseRef) (int64, error) {
	if r.claimToken != "" {
		deadline := time.Now().Add(5 * time.Second)
		for {
			var id, pid int64
			var token, status string
			err := r.db.QueryRowContext(ctx, `select id,claim_token,status,coalesce(pid,0)
				from executor_runs
				where task_id=? and step_id=? and agent_name=? and lease_id=? and lease_version=?`,
				ref.TaskID, ref.StepID, ref.AgentName, ref.LeaseID, ref.LeaseVersion).
				Scan(&id, &token, &status, &pid)
			if err != nil {
				return 0, err
			}
			if token != r.claimToken {
				return 0, errors.New("executor claim ownership changed")
			}
			if status == "running" && pid > 0 {
				r.runtimeEvent(ctx, id, "task.executor.started", map[string]any{"step_id": ref.StepID, "lease_version": ref.LeaseVersion})
				return id, nil
			}
			if time.Now().After(deadline) {
				return 0, errors.New("executor launch confirmation timed out")
			}
			select {
			case <-ctx.Done():
				return 0, ctx.Err()
			case <-time.After(25 * time.Millisecond):
			}
		}
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := r.db.ExecContext(ctx, `insert into executor_runs(task_id,step_id,agent_name,lease_id,lease_version,status,created_at,started_at,updated_at)
		values(?,?,?,?,?,'running',?,?,?)
		on conflict(lease_id) do update set status='running',pid=null,exit_code=null,error_summary='',
			started_at=excluded.started_at,exited_at=null,updated_at=excluded.updated_at`,
		ref.TaskID, ref.StepID, ref.AgentName, ref.LeaseID, ref.LeaseVersion, now, now, now)
	if err != nil {
		return 0, err
	}
	var id int64
	if err := r.db.QueryRowContext(ctx, `select id from executor_runs where lease_id=?`, ref.LeaseID).Scan(&id); err != nil {
		return 0, err
	}
	r.runtimeEvent(ctx, id, "task.executor.started", map[string]any{"step_id": ref.StepID, "lease_version": ref.LeaseVersion})
	return id, nil
}

func (r *Runner) requireExecutionClaim(ctx context.Context, runID int64) error {
	if r.claimToken == "" {
		return nil
	}
	var count int
	err := r.db.QueryRowContext(ctx, `select count(*) from executor_runs
		where id=? and claim_token=? and status='running'`, runID, r.claimToken).Scan(&count)
	if err != nil {
		return err
	}
	if count != 1 {
		return errors.New("executor claim ownership changed")
	}
	return nil
}

func (r *Runner) lastActionSequence(ctx context.Context, runID int64) int {
	var sequence int
	_ = r.db.QueryRowContext(ctx, `select coalesce(max(sequence),0) from executor_actions where run_id=?`, runID).Scan(&sequence)
	return sequence
}

func (r *Runner) auditAction(ctx context.Context, runID int64, seq int, action ActionRequest, value string, actionErr error) {
	status := "passed"
	summary := "ok"
	if actionErr != nil {
		status = "failed"
		summary = Redact(actionErr.Error())
	}
	sum := sha256.Sum256([]byte(value))
	hash := hex.EncodeToString(sum[:])
	target := action.Path
	if action.Type == ActionRunCheck {
		target = formatCheckCommand(CheckRequest{Command: action.Command, Args: action.Args})
	}
	_, _ = r.db.ExecContext(ctx, `insert into executor_actions(run_id,sequence,action_type,relative_path,status,result_summary,result_hash,created_at) values(?,?,?,?,?,?,?,?)`, runID, seq, string(action.Type), target, status, summary, hash, time.Now().UTC().Format(time.RFC3339Nano))
	r.runtimeEvent(ctx, runID, "task.executor.action", map[string]any{"sequence": seq, "action_type": action.Type, "target": target, "status": status, "result_hash": hash})
}

func (r *Runner) fail(ctx context.Context, runID int64, ref leases.LeaseRef, result WorkerResult, runErr error, reason string) (WorkerResult, error) {
	if ctx.Err() == nil && r.freezer != nil {
		freezeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		freezeErr := r.freezer.FreezeStep(freezeCtx, ref.TaskID, ref.StepID, "system", reason)
		cancel()
		if freezeErr != nil && !errors.Is(freezeErr, leases.ErrConflict) {
			runErr = errors.Join(runErr, fmt.Errorf("freeze failed: %w", freezeErr))
		}
	}
	return r.finish(ctx, runID, result, runErr)
}

func (r *Runner) finish(ctx context.Context, runID int64, result WorkerResult, runErr error) (WorkerResult, error) {
	result.FinishedAt = time.Now().UTC()
	message := ""
	if runErr != nil {
		message = Redact(runErr.Error())
	}
	_, _ = r.db.ExecContext(ctx, `update executor_runs set status=?,request_count=request_count+?,input_tokens=input_tokens+?,
		output_tokens=output_tokens+?,error_summary=?,exited_at=?,updated_at=?
		where id=? and (?='' or claim_token=?)`,
		string(result.Status), result.RequestCount, result.InputTokens, result.OutputTokens, message,
		result.FinishedAt.Format(time.RFC3339Nano), result.FinishedAt.Format(time.RFC3339Nano),
		runID, r.claimToken, r.claimToken)
	eventType := "task.executor.exited"
	if runErr != nil {
		eventType = "task.executor.failed"
	}
	r.runtimeEvent(ctx, runID, eventType, map[string]any{"status": result.Status, "request_count": result.RequestCount, "input_tokens": result.InputTokens, "output_tokens": result.OutputTokens, "error": message})
	return result, runErr
}
func (r *Runner) runtimeEvent(ctx context.Context, runID int64, eventType string, payload any) {
	encoded, _ := json.Marshal(payload)
	_, _ = r.db.ExecContext(ctx, `insert into runtime_events(task_id,event_type,actor,payload_json,created_at)
		select task_id,?,agent_name,?,? from executor_runs
		where id=? and (?='' or claim_token=?)`,
		eventType, string(encoded), time.Now().UTC().Format(time.RFC3339Nano), runID, r.claimToken, r.claimToken)
}
func retryableProviderError(err error) bool {
	v := strings.ToLower(err.Error())
	return strings.Contains(v, "429") || strings.Contains(v, "http 5") || strings.Contains(v, "timeout") || strings.Contains(v, "deadline")
}
func providerFailureStatus(err error) RunStatus {
	v := strings.ToLower(err.Error())
	if strings.Contains(v, "provider_type") || strings.Contains(v, "api_key") || strings.Contains(v, "model are required") || strings.Contains(v, "401") || strings.Contains(v, "403") {
		return RunStopped
	}
	return RunFailed
}
