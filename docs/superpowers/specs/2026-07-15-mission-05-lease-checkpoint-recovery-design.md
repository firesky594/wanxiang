# Mission 05 租约、Checkpoint 与恢复设计

## 规范优先级

`wanxiangAgent.md` 是本功能唯一主规范，尤其以第 14 节为准。`wanxiangAgentWorkMission.md` 只记录实施状态、测试证据和后续任务。本文用于补充代码边界和数据流；任何冲突都以前两者为准。

## 目标和边界

M05 建立“数据库任务状态 + Git checkpoint + 简短上下文摘要”的断点续接链路。数据库决定当前写入权，Git 保存可复现代码，摘要告诉原 Agent 或接替 Agent 下一步立即执行什么。

M05 提供租约、心跳、checkpoint、恢复、接管、冻结和审计能力，但不启动 Codex 或其他执行进程。M06 的执行器只能通过 M05 租约调用任务写接口。

## 核心原则

- 同一工作包同一时刻只有一个有效写租约。
- 所有 Agent 任务写操作同时校验身份、task、step、`lease_id`、`lease_version` 和 M04 write scope。
- Git checkpoint 优先由执行 Agent 在自己的分支和 worktree 中创建；Go 服务验证并登记，不自动提交未知文件。
- 无法形成安全提交时，保存上下文摘要和未提交文件清单，保留原 worktree，不执行 `reset`、`clean` 或删除。
- 接替 Agent 使用新分支、新 worktree 和更高版本租约；原 worktree 永远不与接替者共享。
- 聊天记录、日志和模型上下文不能作为唯一恢复来源。

## 数据结构

### 当前租约

`task_steps` 增加当前恢复字段：

- `lease_id`、`lease_version`、`lease_expires_at`、`last_heartbeat_at`。
- `checkpoint_id`、`attempt`、`interrupted_at`、`resume_deadline`。

现有 `agent_name` 表示当前 assigned Agent。工作包状态使用主规范定义的 `assigned`、`in_progress`、`checkpointed`、`interrupted`、`review`、`merged`、`completed` 和 `blocked`。

### 历史记录

新增以下表：

- `task_step_leases`：每次租约的 ID、版本、Agent、状态、branch、worktree、有效期、恢复期限、创建/撤销原因和时间。
- `task_checkpoints`：幂等键、lease、Git 提交、base commit、工作区是否干净、未提交文件、测试、风险和摘要。
- `step_reassignments`：旧租约、新租约、旧 Agent、新 Agent、checkpoint、接管原因和接力 workspace。

当前字段用于原子校验，历史表用于审计。服务重启后只读取数据库和 Git 现场恢复，不依赖内存计时器。

## 租约生命周期

### 领取

只有 `workspace_ready` 任务中、M04 workspace 为 `ready` 且 assignment 属于当前 Agent 的步骤才能领取租约。首次领取生成不可猜测的 `lease_id`，版本为 1，默认 60 秒过期，步骤进入 `in_progress`。重复领取同一有效租约返回原租约，不生成第二份写权限。

### 心跳

任务级心跳默认每 15 秒调用一次。服务使用 `step_id + agent_name + lease_id + lease_version + expected_status` 条件更新，把过期时间延长 60 秒。Agent 在线心跳不能替代任务租约心跳。

### 中断扫描

扫描器周期查询已过期的 active 租约。原子更新成功后，步骤和租约进入 `interrupted`，写入 `interrupted_at`，并设置默认 5 分钟 `resume_deadline`。扫描器重复执行不重复增加 attempt，也不重复发布事件。

### 原 Agent 恢复

原 Agent 在 `resume_deadline` 前携带相同租约 ID 和版本申请恢复。服务依次校验 assignment、workspace、分支、HEAD、最近 checkpoint 和工作区状态。校验通过后恢复相同租约，步骤回到 `in_progress`；失败则保持中断并返回可审计原因。

## Checkpoint 协议

Agent 在主规范规定的时机创建正常 Git 提交，提交说明为 `checkpoint(<step-id>): <中文摘要>`。Agent 再调用 checkpoint API，提交：

- task、step、lease ID、lease version 和幂等键。
- base commit、checkpoint commit、branch 和 worktree 逻辑标识。
- 是否干净、未提交文件清单、测试命令与结果。
- completed、next_action、files_changed、decisions、blockers 和 risks。
- 迁移、密钥、部署或不可逆操作标记。

Go 服务验证 checkpoint commit 存在、属于当前 branch、是 base/provision commit 的后代，并与实际 HEAD 和工作区状态一致。`next_action` 必须非空且是单项可执行动作。摘要经过密钥字段、控制字符、路径和长度校验。

相同 lease 与幂等键重复请求返回原 checkpoint，不重复写 Git 文件、事件或数据库记录。

### 摘要镜像

有效摘要同步到当前 worktree：

```text
.wanxiang/checkpoints/<step-id>/<checkpoint-id>.yaml
```

如果摘要文件已包含在 Agent 提交的 checkpoint 中，服务校验其规范化内容。若安全 checkpoint 无法形成，服务允许创建“上下文型 checkpoint”：checkpoint commit 为空、`clean=false`，摘要文件保留在原 worktree 的未提交现场，步骤进入 `checkpointed` 或 `interrupted`，不能作为自动接管基线。

摘要不得包含密钥、令牌、完整模型对话、用户隐私或无关日志。按照 `wanxiangAgent.md` 第 14.4 节，摘要同时记录绝对 worktree 路径；恢复时必须重新执行根目录、符号链接和 assignment ownership 校验，不能因为摘要中存在该路径就直接信任。

## 超时接管

接管只能由 manager 或管理员触发，并且必须超过 `resume_deadline`，除非管理员明确选择立即接管。

1. 原子撤销旧租约并递增步骤 `lease_version`。
2. 选择或校验新 Agent 的能力、项目权限和在线状态。
3. 查找最近一个 `clean=true`、Git 提交有效的 checkpoint。
4. 从 checkpoint commit 创建 `agent/<new-agent>/<work-item>-resume-<attempt>` 分支和独立 worktree。
5. 新租约记录新的 branch 和 worktree；M04 原 workspace 继续保留为历史现场。
6. 摘要中的最近通过测试由 M06 执行器运行；M05 先把它登记为恢复前置验证。
7. 更新 assignment、step Agent 和接管审计，发布 `task.step.reassigned`。

没有干净 checkpoint、原 worktree 有未审查修改、分支冲突或 Git 校验失败时，接管进入 `blocked: recovery_review`，不得自动复制或覆盖原现场。

## 并发与冲突响应

- 当前租约更新使用单条条件 SQL；受影响行数不是 1 时返回租约冲突。
- 旧 lease ID、旧 version、错误 Agent、错误 step 或 frozen 状态统一返回 HTTP 409，不泄露其他 Agent 的租约详情。
- checkpoint Git 校验在数据库写入前完成；最终写入再次比较 lease version。
- 冻结会撤销写权限但保留现场；解冻必须生成新租约版本，不能复活旧写令牌。
- MR、报告和文件写接口在 M06、M07 接入时必须复用同一个 lease guard。

## API

Agent API：

- 领取工作包租约。
- 任务级心跳。
- 创建或读取 checkpoint。
- 在期限内恢复原租约。
- 查询自身当前租约和恢复摘要。

管理员 API：

- 查询任务全部租约、checkpoint 和接管时间线。
- 延长恢复期限。
- 冻结或解冻工作包。
- 立即撤销并重新分配，可指定接替 Agent。
- 从指定历史 checkpoint 发起接管。

所有管理员变更写入 `audit_logs`；Agent 和扫描器状态变化写入 `runtime_events`。

## 页面

任务详情页在 M04 workspace 轨迹下展示：当前 Agent、租约版本、最后心跳、剩余有效期、恢复期限、最近 checkpoint、next action、attempt 和中断原因。高风险的冻结、立即接管、跳过等待期或从历史 checkpoint 恢复必须显示影响范围并二次确认。

## 测试与验收

- 使用可控时钟测试 15 秒心跳、60 秒过期和 5 分钟恢复窗口，不依赖真实 sleep。
- 并发测试确认同一步骤只能产生一个 active lease。
- 旧 Agent 在接管后使用旧 ID 或 version 写入时返回冲突。
- checkpoint 测试覆盖幂等、祖先关系、错误 branch/HEAD、脏工作区、摘要校验和无密钥输出。
- 服务重启后使用同一 SQLite 和临时 Git 仓库恢复租约、checkpoint、摘要和 deadline。
- 接管测试确认使用新分支和新 worktree，原 worktree 及未提交修改保持不变。
- 完整 Go 测试、后端构建、前端测试和 `web/dist` 构建必须通过，并记录部署与后端重启判断。
