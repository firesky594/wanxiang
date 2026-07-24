# 总管 Agent 治理决定

## 固定角色

- `manager` 是平台总管，始终保留。
- 子 Agent 最低角色固定为 `main-backend-engineer`、`main-frontend-engineer`、
  `main-test-engineer`、`main-technical-manager`、`main-ui-analyst` 和
  `main-operations-engineer`；缺少时仅创建无密钥骨架并注册为
  `blocked: missing_config`，不得覆盖已有文件或状态。
- 长期 Agent 使用 `main-功能名称`；任务或步骤内创建的 Agent 使用
  `sub-任务或步骤ID-功能名称`。

## 巡检原则

- 服务每 15 秒确定性检查项目、任务、步骤和 Agent 状态。
- 运行快照写入 `memory/summaries/runtime-status.md`，只有状态指纹变化时才记录运行事件。
- 巡检不调用 Manager 规划模型，也不复制、记录或写出密钥；仅由配置服务读取 Agent 自身 env，
  对 `configured` Agent 做 Provider 连通性探测，失败后保留 `blocked: provider_error`，
  不在每轮巡检中重复调用，而是从 15 秒开始指数退避，最长 5 分钟后重试；配置重新进入
  `configured` 时立即探测。
- 仅数据库已经是 `online`、但未在 Launcher 活动集合中的 Agent 可无模型调用地恢复心跳。
  `configured`（包括完整 env 修复后的旧阻塞 Agent）必须探测成功后才能进入 `online`。

## 配置与能力

- 优先复用现有 Agent，并保留其专业角色、Memory、Skill、MCP 和工作现场。
- Skill/MCP 只有实际出现在对应资源目录中才视为已安装，不能创建空占位或在规划中虚构。
- 查找缺失资源时先使用可信库存或目录；下载、安装、外部授权和权限扩大必须取得用户确认。
- 缺少 Provider、模型、密钥、能力、Skill、MCP 或项目权限时保持阻塞，并向用户说明缺口。
- 不生成、猜测、复制或借用任何 Agent 的密钥。

## 删除边界

- 自动巡检不删除 Agent，只能提出闲置候选。
- Manager、最低角色最后一个有效实例，以及仍有关联任务、租约、执行进程、Worktree、分支、
  MR、Memory、Skill、MCP 或已配置密钥的 Agent 不能作为自动清理目标。
- 为完成当前任务，可在其余硬性条件都满足且匹配记录可审计时，仅追加当前任务所属的精确项目
  标识；通配符、批量跨项目扩权，以及删除、归档或吊销身份前，必须列出精确对象与影响范围并
  取得用户确认。

## 项目闭环

- 新项目持续检查规划、分配、工作区、执行、审核与本地合并状态；阶段失败必须留下可见原因和
  有界重试信息，不能用重复任务、重复决策或伪造成功掩盖阻塞。
- 完成报告由数据库登记的项目负责人使用独立审核调用处理。只有检查点、真实测试证据、依赖、
  阻塞 Issue 和风险门禁全部通过，才允许批准并合入本地 `main`。
- 高风险、未完成事项或需要用户决策的报告不自动批准；必须保持可审计阻塞并等待明确确认。
