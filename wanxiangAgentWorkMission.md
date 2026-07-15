# Wanxiang Agent 完整链路实施任务

> 文档状态：后续 AI 的任务入口和交接记录
> 最后核对：2026-07-14
> 规则来源：`wanxiangAgent.md`

## 1. 使用方法

后续 AI 每次开始工作时按以下顺序执行：

1. 阅读 `wanxiangAgent.md`，确认角色、权限和完整调度链路。
2. 阅读本文档，选择第一个状态不是“已完成”且依赖已经满足的 Mission。
3. 检查 Git 分支、工作区、相关代码和最近提交，不覆盖其他人的修改。
4. 把 Mission 状态改为“进行中”，记录执行者、分支和起始提交。
5. 按验收标准实施并运行指定测试。
6. 创建 Git checkpoint 或正式提交，填写完成证据、测试结果、风险和下一步。
7. 只有代码、测试、文档和运行链路同时满足验收标准，才能把 Mission 标记为“已完成”。

状态只使用：`未开始`、`进行中`、`阻塞`、`待审核`、`已完成`。

每个 Mission 的交接记录使用以下格式：

```yaml
status: 进行中
agent: manager
branch: agent/manager/mission-01
base_commit: abc1234
checkpoint_commit: def5678
completed:
  - 已完成事项
tests:
  - command: go test ./internal/tasks
    result: passed
risks: []
frontend_build_required: false
frontend_build_result: not_required
backend_build_required: true
backend_build_result: passed
backend_restart_required: true
backend_restarted: false
backend_restart_reason: 尚未替换运行中的后端二进制
next_action: 下一项可立即执行的动作
```

不得填写密钥、令牌、完整模型对话或用户隐私。

## 2. 当前链路核对

| 链路节点 | 状态 | 当前实现和缺口 |
| --- | --- | --- |
| 管理员初始化、登录和会话 | 已实现 | `server/internal/httpapi/handlers_auth.go` 和 `auth/` 已覆盖 |
| 用户提交任务 | 已实现 | 可新建项目或按已登记项目 ID 复用，复用前校验路径、main 分支和干净状态 |
| 总管理解目标和拆分工作包 | 已实现 | planning Worker 调用 manager Provider 并校验结构化规划 |
| 生成工作包和依赖图 | 已实现 | 规划结果事务化写入 `task_steps`、`workflow_edges`、摘要和事件 |
| 估算 Agent 数量和并发度 | 部分实现 | 匹配时按工作包和 `max_concurrency` 分配；尚无独立估算结果 |
| 决定动态项目负责人 | 部分实现 | 已持久化负责人和汇报关系；后续补充执行期替换与权限逻辑 |
| 匹配 Agent | 已实现 | 按在线状态、能力、Skill、MCP、项目权限、并发和负载过滤并解释评分 |
| 创建缺失 Agent | 已实现 | 无候选时生成不含密钥的 Agent 骨架并进入 `blocked: missing_config` |
| Provider 真实探测 | 已实现 | OpenAI 和 DeepSeek 均有适配器和测试 |
| 配置完成后恢复调度 | 部分实现 | planning 和 matching 均可自动恢复；执行、审核阶段待后续 Mission 接入 |
| 创建或选择 Project | 已实现 | 管理台与后端支持新建或按数据库 ID 复用已有项目，不接受任意客户端路径 |
| `project.yaml` 和 assignments | 已实现 | 数据库运行状态与 `.wanxiang/` Git 快照双向印证，漂移不静默覆盖 |
| Agent 独立分支和 worktree | 已实现 | 自动创建独立分支/worktree，登记双提交、范围和状态，支持校验与确认清理 |
| Agent 任务租约和断点恢复 | 未实现 | 只有 Agent 在线心跳，没有任务级租约、checkpoint 和恢复器 |
| 启动执行 Agent | 未实现 | Launcher 只执行 Provider 探测和心跳，不启动 Codex、CLI 或 Agent 进程 |
| Agent 消费任务并修改代码 | 未实现 | 没有任务队列、命令执行器或项目写入协议 |
| Token 用量、记忆和日志 | 部分实现 | 有写入接口；没有与任务租约和 scope 绑定 |
| 完成报告 | 未实现 | 没有结构化报告、测试证据和交接摘要服务 |
| 创建 MR | 部分实现 | Agent API 可创建；管理台使用管理员会话调用 Agent API，鉴权不通 |
| 阻塞 Issue | 已实现 | 管理员可创建，阻塞 Issue 会阻止 MR 合并 |
| 项目负责人审核 | 未实现 | 当前只有身份为 `manager` 的 Agent 能合并 `main` |
| 本地合并 | 已实现 | 校验分支和干净工作区，使用 `--no-ff`，冲突后 abort |
| 总管汇总和用户验收 | 未实现 | 没有结果汇总、验收或返工状态流转 |
| 自动测试、重试、回滚和发布 | 未实现 | 没有编排服务 |
| 查询列表和刷新恢复 | 已实现 | 管理员 API 提供任务、项目、MR、Issue 和事件查询；任务页先加载持久快照再连接 SSE |
| Agent scope 权限 | 部分实现 | workspace ownership 已校验 Agent、task、step 和路径范围；M05 写接口继续叠加 lease 与 token scope |

当前实际链路停在：

```text
管理员创建任务
  -> 写入 projects/tasks
  -> 创建 projects/<task>/
  -> 初始化 main
  -> 写入 .wanxiang/task.yaml
  -> 发布 task.created
  -> planning Worker 生成工作包和依赖图
  -> matching Worker 过滤、评分并保存 assignment
  -> 有候选时进入 assigned；无候选时进入 blocked: missing_config
  -> workspace Worker 提交 Git 元数据并创建独立分支和 worktree
  -> 数据库、Git 快照和 worktree 校验一致后进入 workspace_ready
  -> 当前尚未建立任务租约、checkpoint 或启动执行 Agent
```

## 3. 实施顺序

后续 Mission 按依赖顺序执行：

```text
M01 查询和状态基础
  -> M02 规划协议
  -> M03 Agent 匹配
  -> M04 项目元数据与 assignments
  -> M05 租约与 checkpoint
  -> M06 Agent 执行器
  -> M07 报告、MR 和审核
  -> M08 总管汇总与用户验收
  -> M09 重试、回滚和发布
  -> M10 端到端验收与安全加固
```

M05 的数据库结构可以与 M02 的任务步骤设计一起评审，但必须在 M06 启动真实执行器前完成。

## 4. Mission 清单

### M01：任务查询和状态基础

**状态：已完成**

目标：让服务和管理台能在刷新后重新读取任务、项目、步骤、MR、Issue 和事件，不再只依赖当前 SSE 内存。

实施范围：

- 为 tasks、projects、task_steps、workflow_edges、merge_requests、issues 增加列表和详情查询服务。
- 增加管理员查询 API，并执行分页、排序和资源存在性校验。
- 任务详情页加载持久数据，再订阅 SSE 增量事件。
- 定义任务和工作包的合法状态转换，拒绝跳跃更新。

验收：

- 服务重启和浏览器刷新后能看到原任务及其事件。
- 无效 ID 返回 404，非法状态转换返回 409。
- 后端服务、HTTP API 和前端 Store 都有测试。

### M02：总管规划循环

**状态：已完成，依赖 M01（已满足）**

目标：manager 消费 `created` 任务，调用模型生成可验证的结构化规划。

实施范围：

- 定义规划输入和 JSON Schema，包含目标、约束、验收标准、工作包、依赖和风险。
- 读取 manager 的 `system_prompt.md`，把任务规则和项目上下文传给 Provider。
- 校验模型输出后写入 `task_steps` 和 `workflow_edges`。
- 模型输出无效时记录安全错误摘要并进入 `blocked: planning_error`。
- 使用幂等规划键，避免服务重启后重复创建工作包。

验收：

- 一个任务只生成一套有效工作包和依赖图。
- Provider 超时、无效 JSON 和缺失字段都有确定状态与事件。
- 规划请求和日志不包含任何 Agent 密钥。

### M03：Agent 能力匹配和团队决策

**状态：已完成，依赖 M02（已满足）**

目标：根据硬性条件和排序条件为工作包选择 Agent，并决定是否需要项目负责人。

实施范围：

- 运行时读取 `agent.yaml`、skills、mcps 和允许的记忆摘要。
- 校验 Provider 在线、能力、MCP、Skill、项目权限、并发上限和当前负载。
- 记录候选评分和最终选择理由。
- 没有合适 Agent 时生成非密钥定义，标记 `blocked: missing_config`。
- 用户完成配置和 Probe 后自动恢复匹配流程。

验收：

- 不满足硬条件的 Agent 不会得到工作包。
- 用户能看到选择理由并覆盖选择。
- 需要共享接口、集成或高风险复核时生成项目负责人决策。

```yaml
status: 已完成
agent: Codex
branch: feat/mission-03
base_commit: f24e98c
checkpoint_commit: 9409c22
completed:
  - 实现非密钥 Agent 定义加载、硬条件过滤和稳定解释评分
  - 持久化匹配决策、工作包 assignment、负责人和汇报关系
  - 无候选时生成安全 Agent 骨架，配置 Probe 成功后自动恢复匹配
  - 增加管理员查询、覆盖 API 和任务详情匹配轨迹
tests:
  - command: GOCACHE=/tmp/wanxiang-go-cache go test ./...
    result: passed
  - command: GOCACHE=/tmp/wanxiang-go-cache go build -buildvcs=false -o /tmp/wanxiang-m03-bin ./cmd/wanxiang
    result: passed
  - command: npm test
    result: 8 passed
  - command: npm run build
    result: passed，已生成 web/dist；存在非阻塞的大 chunk 警告
risks:
  - 执行期负责人替换、任务租约和独立 worktree 由 M04、M05 实现
frontend_build_required: true
frontend_build_result: passed
frontend_deployed: false
frontend_deploy_reason: web/dist 已验证但未替换线上静态资源
backend_build_required: true
backend_build_result: passed
backend_restart_required: true
backend_restarted: false
backend_restart_reason: 尚未替换运行中的后端二进制，不应仅因源码合并重启旧进程
next_action: 开始 M04，生成项目元数据、分支策略和独立 worktree
```

### M04：项目元数据、assignment、分支和 worktree

**状态：已完成，依赖 M03（已满足）**

```yaml
status: 已完成
agent: Codex
branch: feat/mission-04
base_commit: cb231e8
checkpoint_commit: 6547ebf
completed:
  - 支持安全复用已登记且干净的 main 项目
  - 增加 project_workspaces 数据状态和唯一约束
  - 实现确定性 project 与 assignment YAML、哈希和安全校验
  - 实现幂等 provision、中文元数据提交和独立 Agent worktree
  - 支持 provision 中断恢复并拒绝未知分支或非空目录
  - 实现数据库、Git 快照、分支和 worktree 双向漂移检测
  - 实现显式修复方向、审计记录和确认式安全清理
  - 接通 workspace 自动 provision 与周期漂移校验 Worker
  - 增加管理员 workspace API 和 Agent assignment ownership 校验
  - 管理台支持按项目 ID 复用项目并展示 workspace 漂移与清理操作
tests:
  - command: GOCACHE=/tmp/wanxiang-go-cache go test -count=1 -timeout=60s ./...
    result: passed，所有 Go 包完成
  - command: GOCACHE=/tmp/wanxiang-go-cache go build -buildvcs=false -o /tmp/wanxiang-m04-bin ./cmd/wanxiang
    result: passed
  - command: npm test
    result: 10 passed
  - command: npm run build
    result: passed，已生成 web/dist；存在非阻塞的大 chunk 警告
risks:
  - 项目级 provision 锁当前为单进程锁，多实例部署前需升级为数据库锁
  - 清理 worktree 后保留 Agent 分支，后续由 MR 和保留策略决定删除时机
frontend_build_required: true
frontend_build_result: passed
frontend_deployed: false
frontend_deploy_reason: web/dist 已验证但未替换线上静态资源
backend_build_required: true
backend_build_result: passed
backend_restart_required: true
backend_restarted: false
backend_restart_reason: 仅构建了 /tmp/wanxiang-m04-bin，尚未替换运行中的 server/wanxiang
next_action: 开始 M05，建立任务租约、Git checkpoint 和上下文摘要恢复链路
```

目标：建立可执行、可审计的项目范围和 Git 隔离环境。

实施范围：

- 支持选择已有项目或创建新项目。
- 生成 `.wanxiang/project.yaml`、assignments、分支策略和汇报对象。
- 为每个执行 Agent 创建 `agent/<agent>/<work-item>` 分支和独立 worktree。
- 登记起始提交、worktree 路径和写入范围。
- 使用路径校验和 Agent 身份阻止跨项目写入。

验收：

- 两个 Agent 不会共享开发分支或 worktree。
- assignment 外的 Agent 不能修改项目或任务状态。
- 删除和清理 worktree 必须经过完成或人工确认流程。

### M05：任务租约、Git checkpoint 和上下文摘要

**状态：进行中，依赖 M01、M04（已满足）**

```yaml
status: 进行中
agent: manager
branch: feat/mission-05
base_commit: ec90721
checkpoint_commit: 7764969
completed:
  - Task 1 已增加 task_steps 租约、心跳、checkpoint 和恢复字段的幂等迁移
  - Task 1 已增加租约、checkpoint、接管历史表及唯一约束和查询索引
  - Task 1 已增加租约状态、公开视图、系统时钟和可推进 fake clock
  - Task 2 已实现事务化租约领取、幂等重领和并发单租约约束
  - Task 2 已实现精确身份与版本心跳续期，以及叠加 workspace scope 的统一 Lease Guard
  - Task 3 已实现 Git branch、HEAD、祖先关系和工作区 clean 状态校验
  - Task 3 已实现幂等 checkpoint、SHA-256 摘要、受控镜像文件和 checkpoint 事件
  - Task 3 已实现短上下文脱敏、长度/控制字符/路径校验及脏现场保留
  - Task 4 已实现租约过期扫描、幂等中断事件和 5 分钟恢复期限
  - Task 4 已实现进程启动立即扫描、持久状态重载和原 Agent 同版本恢复
  - Task 4 已实现 checkpoint、branch、HEAD 与脏文件清单互相印证的恢复校验
  - Task 5 已实现冻结立即撤权、解冻换发新版本租约和恢复期限延期审计
  - Task 5 已实现从最近干净 checkpoint 为在线接替 Agent 创建新分支和独立 worktree
  - Task 5 已保留原 worktree 与脏文件，并在缺少安全基线、分支冲突或 Git 基线无效时阻塞人工审查
  - Task 6 已接通 Agent 租约领取、心跳、checkpoint、恢复与自身查询 API，并以认证身份覆盖请求体身份
  - Task 6 已接通 Admin 时间线、延期、冻结、解冻、指定 checkpoint 接管 API 和恢复 Worker 生命周期
  - Task 6 已在任务详情增加租约剩余时间、attempt、checkpoint、next action 与高风险二次确认操作
tests:
  - command: GOCACHE=/tmp/wanxiang-go-cache go test ./internal/db ./internal/leases -run 'Migrate|LeaseTypes'
    result: passed
  - command: GOCACHE=/tmp/wanxiang-go-cache go test ./internal/db ./internal/leases ./internal/workspaces
    result: passed
  - command: GOCACHE=/tmp/wanxiang-go-cache go test ./internal/leases -run Checkpoint
    result: passed
  - command: GOCACHE=/tmp/wanxiang-go-cache go test ./internal/leases -run 'Interrupt|Resume|Worker'
    result: passed
  - command: GOCACHE=/tmp/wanxiang-go-cache go test ./internal/leases -run 'Freeze|Deadline|Reassign'
    result: passed
  - command: GOCACHE=/tmp/wanxiang-go-cache go test -count=1 -timeout=60s ./...
    result: passed，首次受限沙箱禁止 httptest 本机端口，获准后复跑全量通过
  - command: GOCACHE=/tmp/wanxiang-go-cache go build -buildvcs=false -o /tmp/wanxiang-m05-task5-bin ./cmd/wanxiang
    result: passed
  - command: GOCACHE=/tmp/wanxiang-go-cache go test ./internal/leases ./internal/httpapi ./internal/app
    result: passed
  - command: npm test -- --run
    result: passed，4 个测试文件共 11 项
  - command: npm run build
    result: passed，已生成 web/dist；存在非阻塞的大 chunk 警告
risks:
  - Mission 合并前仍需全量复核、代码审查，并确认生产静态资源与 PM2 二进制是否部署
frontend_build_required: true
frontend_build_command: npm test -- --run && npm run build
frontend_build_result: passed
frontend_dist_path: web/dist
frontend_deployed: false
frontend_deploy_reason: 已验证功能分支构建产物，但尚未替换线上静态资源
backend_build_required: true
backend_build_command: GOCACHE=/tmp/wanxiang-go-cache go test -count=1 -timeout=60s ./... && GOCACHE=/tmp/wanxiang-go-cache go build -buildvcs=false -o /tmp/wanxiang-m05-task5-bin ./cmd/wanxiang
backend_build_result: passed
backend_restart_required: true
backend_restarted: false
backend_restart_reason: 当前只提交功能分支源码，尚未构建并替换 PM2 指向的生产二进制，不得重启旧进程
backend_process_manager: pm2
backend_pm2_app: wanxiang-agent
backend_pm2_status: not_checked
backend_healthcheck_result: not_checked
next_action: 执行 Task 7 全量验证、代码审查、交接更新并合并到 main
```

目标：实现 `wanxiangAgent.md` 第 14 节规定的完整断点续接协议。

实施范围：

- 为工作包增加租约、版本、心跳、中断、恢复期限和 checkpoint 数据。
- 增加任务级心跳、checkpoint、恢复、撤销租约和重新分配 API。
- checkpoint 同时保存 Git 提交、工作区状态、测试结果和短摘要。
- 增加恢复扫描器，服务启动后扫描过期租约。
- 原 Agent 在期限内校验现场后恢复；超时后总管创建接力分支和独立 worktree。
- 所有写接口执行 `lease_id + lease_version + scope` 校验。

验收：

- 杀死执行 Agent 后，工作包在 60 秒后进入 `interrupted`。
- 原 Agent 在恢复期限内从 `next_action` 继续，已完成步骤不重复。
- 新 Agent 接管后，旧 Agent 的写请求返回 409。
- 服务重启不丢失租约、checkpoint、摘要和恢复期限。
- 含未提交修改的原 worktree 不会被自动删除或覆盖。

### M06：Agent 执行器和任务消费

**状态：未开始，依赖 M02 至 M05**

目标：启动真实 Agent 进程，让它在分配的 worktree 中消费工作包、运行命令并报告状态。

实施范围：

- 定义执行器接口，首个实现负责启动和监管本地 Codex/CLI 进程。
- 运行时注入 Agent 自身 Provider 配置、任务令牌和项目范围。
- 只把 Agent 分配到自己的 worktree，不给平台根目录写权限。
- 捕获标准输出、退出码、心跳、Token 用量和检查点请求。
- 进程异常退出时触发 M05 中断流程。

验收：

- Agent 能领取工作包、修改分支、运行测试并产生 checkpoint。
- 越界路径和未授权命令被服务拒绝并写入审计日志。
- Agent 退出不会让任务永久停在 `in_progress`。

### M07：完成报告、MR 和审核链路

**状态：未开始，依赖 M03 至 M06**

目标：让执行 Agent 提交结构化完成报告，并按项目负责人决策进入审核和合并。

实施范围：

- 持久化完成事项、未完成事项、提交、测试、风险、依赖和用户决策。
- 修复管理台 MR 鉴权：管理员操作使用管理员 API，Agent 操作使用 Agent API。
- 校验 MR 的任务、项目、分支、租约和创建者关系。
- 增加项目负责人审核、退回和合并权限。
- 保留阻塞 Issue、干净工作区、冲突 abort 和 `--no-ff` 规则。

验收：

- 浏览器管理员会话能执行授权的审核操作。
- 执行 Agent 不能伪造 `created_by` 或合并 `main`。
- 多 Agent 项目由负责人按依赖顺序集成；单 Agent 项目由总管审核。

### M08：总管汇总、用户验收和返工

**状态：未开始，依赖 M07**

目标：总管把项目结果转换成用户可验收的交付，并支持返工回到规划阶段。

实施范围：

- 汇总提交、测试、风险、阻塞和未完成事项。
- 用户可验收、拒绝、提出调整或扩大权限。
- 返工生成新的工作包版本，保留原计划、报告和提交关系。
- 只有所有必需工作包合并并通过验收，任务才能进入 `completed`。

验收：

- 用户能追溯最终结果到工作包、Agent、提交和测试。
- 拒绝验收不会篡改已完成历史。
- 高风险动作仍需要用户单独确认。

### M09：测试、重试、回滚和发布编排

**状态：未开始，依赖 M08**

目标：为不同项目定义可审核的集成测试、有限重试、回滚和发布流程。

实施范围：

- 从项目元数据读取测试和构建命令。
- 区分代码失败、环境失败、Provider 失败和权限阻塞。
- 使用有上限的重试策略，连续失败后创建阻塞 Issue。
- 保存发布前提交和回滚入口。
- 部署、删除数据和生产迁移始终等待用户确认。

验收：

- 重试不会重复执行已经成功的不可逆步骤。
- 发布失败能回到已记录的安全版本。
- 未经用户确认不会触发生产部署或数据删除。

### M10：端到端验收和安全加固

**状态：未开始，依赖 M01 至 M09**

目标：证明自然语言任务能够经过完整链路交付，并验证权限、恢复和密钥边界。

验收场景：

- 单 Agent 任务从创建、规划、执行、MR、合并到用户验收。
- 多 Agent 任务包含项目负责人、依赖分支和集成测试。
- 缺少 Provider 配置时阻塞，用户配置成功后自动恢复。
- Agent 执行中被强制终止，原 Agent 恢复和新 Agent 接管各验证一次。
- 阻塞 Issue 阻止合并，解除后继续。
- 过期租约、越界路径、无 scope Token 和伪造身份均被拒绝。
- API 响应、事件、日志、checkpoint 和 Git 历史不出现密钥。

完成标准：

- 后端、前端和端到端测试全部通过。
- 任务时间线能展示每次规划、分配、checkpoint、中断、恢复、审核和验收。
- `wanxiangAgentWorkMission.md` 的链路核对、Mission 状态和代码证据按真实实现同步更新；`wanxiangAgent.md` 只保留规范。

## 5. 当前交接记录

```yaml
status: 进行中
agent: manager
branch: feat/mission-03
base_commit: f24e98c
checkpoint_commit: null
completed:
  - 已完成完整链路代码核对
  - 已定义断点续接方案
  - 已拆分 M01 至 M10
  - 已完成 M01 任务、项目、MR、Issue 和事件持久查询
  - 已完成任务状态转换、404、409 和分页校验
  - 已完成前端持久任务 Store 和历史事件恢复
  - 已完成 M02 结构化规划、依赖校验和脱敏错误处理
  - 已完成规划事务写入、幂等读取和 manager 就绪后自动消费
tests:
  - command: GOCACHE=/tmp/wanxiang-m01-go-cache go test ./...
    result: passed
  - command: npm test -- --run
    result: 7 passed
  - command: npm run build
    result: passed with existing chunk-size warning
  - command: GOCACHE=/tmp/wanxiang-m02-go-cache go test ./...
    result: passed
  - command: GOCACHE=/tmp/wanxiang-m02-go-cache go build -buildvcs=false -o wanxiang ./cmd/wanxiang
    result: passed
risks:
  - manager 未完成配置或 Provider 不可用时，created 任务会等待，不会进入规划
  - 管理台 MR 页面与 Agent API 使用不同认证方式
  - Agent Token scopes 尚未执行
frontend_build_required: false
frontend_build_result: 回归测试和构建通过；M02 未修改 Web 文件或 HTTP 契约
frontend_dist_path: web/dist
frontend_deployed: false
backend_build_required: true
backend_build_result: 通过；linked worktree 使用 buildvcs=false 构建
backend_restart_required: true
backend_restarted: false
backend_restart_reason: 本次只完成源码并构建隔离 worktree 二进制，尚未替换生产运行目录中的 server/wanxiang
next_action: 按 docs/superpowers/plans/2026-07-14-mission-03-agent-matching.md 执行 Task 1
```
