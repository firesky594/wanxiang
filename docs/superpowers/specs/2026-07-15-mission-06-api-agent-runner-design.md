# Mission 06 API Agent 执行器设计

## 规范优先级

`wanxiangAgent.md` 是唯一主规范，`wanxiangAgentWorkMission.md` 只记录实现状态和交接证据。M06 必须复用 M04 独立 worktree 和 M05 租约、checkpoint、恢复协议，不能建立旁路写权限。

## 目标与边界

M06 启动由 Go 主服务监管的独立 Agent Worker 进程。每个 Worker 使用自身 `agents/<agent>/env` 中的 Provider、模型、Base URL 和密钥调用远程 API，在自己的 worktree 内消费一个工作包，并通过 M05 接口报告心跳、checkpoint、Token 用量、日志和退出状态。

M06 不调用本机 Codex、OpenCode 或其他 AI CLI，不允许模型直接获得 shell，也不负责 M07 的完成报告、MR 和审核。

## 运行架构

主服务新增 Executor Supervisor：

1. 从数据库选择依赖已满足、assignment 和 workspace 均 ready 的步骤。
2. 领取或确认 M05 租约，生成短期、仅限当前 task/step 的 Agent Token。
3. 使用当前 `wanxiang-agent` 二进制启动 `agent-worker` 子进程；一个工作包对应一个进程。
4. 子进程只接收非密钥任务参数。自身 Provider 密钥通过子进程环境注入，不出现在命令参数。
5. 子进程调用自身 Provider API，返回严格 JSON 动作；主服务验证后执行受控工具。
6. Worker 每 15 秒续租，正常退出前创建 checkpoint；异常退出由 M05 扫描器转入 `interrupted`。

PM2 只管理主后端 `wanxiang-agent`。Worker 是主服务的临时子进程，不注册为独立 PM2 应用。

## Agent 配置与低量测试

- 每个 Agent 只使用自己的 `agents/<agent>/env`，运行时不得回退读取 manager env。
- 测试阶段允许把 `agents/manager/env` 原样复制到已创建的目标 Agent 目录，文件权限固定为 `0600`。
- 复制只在显式低量测试引导动作中发生；目标 env 已存在时拒绝覆盖，manager env 永不改写。
- `agents/*/env` 保持 Git 忽略。复制内容不得写入数据库、日志、事件、checkpoint、摘要、命令参数或 API 响应。
- 当前运行数据库只有 manager；其他 Agent 由既有匹配流程按 `wanxiangAgent.md` 创建后，才允许执行测试复制。
- 低量测试默认：并发 1、单工作包、最多 3 次 Provider 请求、输入与输出 token 分别限制、禁止部署和不可逆操作。模型由各 Agent env 决定，测试时可以相同。

## Worker 协议

Worker 输入只包含：task ID、step ID、Agent 名称、租约 ID/version、父服务 loopback 地址和短期 Agent Token。禁止把 Provider 密钥放入输入 JSON或命令参数。

Worker 请求 Provider 时发送：

- 工作包标题、描述、验收标准和依赖摘要。
- `.wanxiang/` 项目/assignment 元数据的安全字段。
- 最近 checkpoint 的 `completed`、`next_action`、decisions、blockers 和 risks。
- 通过受控读取工具获得的必要文件片段。

Provider 必须返回版本化 JSON：`status`、`summary`、`actions`、`next_action`。未知字段可以忽略，未知动作、控制字符、绝对路径、路径穿越、超长内容或疑似密钥内容必须拒绝。

## 受控工具

首期仅提供：

- `read_file`：读取 write scope 内的普通文件，限制单文件和总字节数，拒绝符号链接。
- `write_file`：写入 scope 内文件，使用临时文件和原子替换，禁止 `.git`、Agent env、平台源码和部署配置。
- `run_check`：只运行 assignment/项目元数据明确允许的测试命令；参数数组直传，不经过 shell。
- `git_status`：读取当前分支、HEAD 和变更清单。
- `checkpoint`：按 M05 规则提交中文 Git checkpoint 并登记短摘要。

所有写入先执行 Agent 身份、task、step、lease ID/version 和 M04 scope 校验。模型输出永远不能直接传给 `sh -c`、`bash -c`、PowerShell 或等价 shell。

## 进程与并发

- Supervisor 使用数据库状态决定可运行步骤，并遵守 Agent `max_concurrency` 和全局低量测试上限。
- 同一步骤只允许一个活跃 Worker；服务重启后根据租约和 `executor_runs` 恢复判断，不重复启动。
- 主服务保存 PID、启动时间、退出码、最后心跳和脱密错误摘要，但不保存完整模型对话。
- 关闭主服务时先停止领取新任务，通知 Worker 创建 checkpoint，在超时后终止子进程；App Close 等待监管 goroutine 退出。
- Worker 被杀死、Provider 超时或网络中断时，不自动复制现场，由 M05 负责中断与恢复。

## 数据与审计

新增 `executor_runs` 保存 task、step、Agent、lease、PID、状态、请求次数、Token 汇总、退出码和时间；新增 `executor_actions` 保存动作类型、相对路径、结果、摘要哈希和时间。两表禁止保存 Provider 密钥、完整请求/响应或文件完整内容。

关键事件包括：`task.executor.started`、`task.executor.action`、`task.executor.checkpointed`、`task.executor.exited` 和 `task.executor.failed`。错误消息经过密钥模式过滤和长度限制。

## 错误处理

- Agent env 缺失或无效：`blocked: missing_config`，不启动 Worker。
- Provider 401/403：`blocked: provider_error`，不把响应中的凭据相关内容写日志。
- Provider 超时、429 或 5xx：在请求预算内有限重试；预算耗尽后停止 Worker，租约进入中断流程。
- JSON 或动作非法：拒绝该动作并记录脱密审计；不执行部分未知命令。
- 租约冲突或被冻结：Worker 立即停止写入并退出，旧租约不得继续。
- 测试失败：保存结果和 next action，创建上下文 checkpoint，不伪造通过状态。

## 验收

- 测试 Agent 的 env 从 manager env 显式复制且为 `0600`，已有目标 env 不被覆盖，manager env 内容与权限不变。
- 两个独立 Worker 可由 Go Supervisor 并行监管，但低量验收默认并发 1。
- Worker 只调用远程 Provider API，测试证明不会调用 `codex`、`opencode` 或 shell。
- Worker 只能读写自己的 worktree/scope，旧租约、越界路径、符号链接和未授权命令均被拒绝。
- 杀死 Worker 后 M05 在 60 秒后中断；服务重启不重复启动同一租约。
- 数据库、事件、日志、Git、checkpoint、进程参数和 API 响应的扫描结果不包含密钥。
- 完成 Go 全量测试和构建；若管理台增加执行时间线，则完成前端测试和 `web/dist` 构建。
- 只有新二进制替换 PM2 指向的 `server/wanxiang` 后才重启 `wanxiang-agent` 并验证 PM2 与 `/api/health`。
