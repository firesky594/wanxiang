# M08 总管汇总、用户验收与返工设计

日期：2026-07-15
状态：已确认设计，等待书面规格复核

## 1. 目标

M08 把 M07 的项目合并结果转换成用户可验收的交付版本。manager 消费结构化通知，生成不可变交付快照；用户通过管理员会话验收、拒绝或要求调整。系统保留旧快照和决定，返工创建新轮次并重新进入规划链路。

M08 以可恢复进度为硬性要求。每个实现任务结束后必须更新 Mission checkpoint、提交中文 commit 并推送功能分支。任何后续 Agent 都能从数据库状态、Git checkpoint 和 Mission 的唯一 `next_action` 继续。

## 2. 范围

M08 实现：

- manager 通知消费和交付快照生成。
- 交付版本的提交、MR、报告、测试、风险、未完成项和用户决策汇总。
- 用户验收、拒绝和要求调整。
- 返工轮次、工作包版本和原交付版本关联。
- 任务状态 `awaiting_acceptance`、`completed`、`rework_planning`。
- 高风险事项转为独立阻塞 Issue，验收不能顺带授权。
- 管理员交付验收页面、历史版本和返工轨迹。
- 中断后从持久状态继续消费、验收或规划返工。

M08 不执行部署、删除数据、生产迁移、自动回滚或发布编排。这些操作属于 M09，仍需用户单独确认。

## 3. 数据模型

### 3.1 `delivery_snapshots`

每条记录是不可变交付版本，包含：

- `id`、`task_id`、`project_id`、`version`、`status`。
- 触发该快照的 `manager_notification_id` 和项目 `main_commit`。
- 已合并 MR、报告、工作包、Agent、提交、测试、风险、未完成项和用户决策的 JSON 汇总。
- `summary`、`summary_hash`、`created_by`、`created_at`。

同一任务版本唯一，同一 manager 通知最多生成一个快照。状态限定为 `awaiting_acceptance`、`accepted`、`rejected`、`revision_requested`、`superseded`。

### 3.2 `acceptance_decisions`

每次用户决定追加记录，不覆盖历史：

- `snapshot_id`、`task_id`、管理员身份。
- `decision`：`accepted`、`rejected`、`revision_requested`。
- 意见、创建时间和幂等键。

同一幂等键只执行一次。已接受快照不能再次决定。

### 3.3 `rework_rounds`

拒绝或要求调整时创建返工轮次：

- 原快照、任务、轮次编号和原因。
- 新规划版本、新工作包 ID 列表和状态。
- `created_by`、创建时间、恢复 checkpoint 和最后错误。

返工轮次状态为 `planning`、`planned`、`blocked`、`completed`。旧步骤、报告、MR、通知、快照和决定保持不变。

### 3.4 `task_plan_versions`

现有 `task_steps` 缺少计划版本。M08 增加 `task_plan_versions`，并给 `task_steps` 增加 `plan_version`：

- 初始工作包归属版本 1。
- 每次返工创建下一版本。
- M02 规划 Worker 只向新版本写步骤和依赖，不修改旧版本。

## 4. manager 汇总

后台 Worker 扫描 `manager_notifications.status='pending'`：

1. 锁定一条通知并检查幂等关系。
2. 校验任务所有当前计划版本必需步骤均为 `completed`。
3. 校验相关 MR 均为 `merged`，没有未解决阻塞 Issue。
4. 读取 M07 报告、审核、测试、风险、未完成项和用户决策。
5. 生成结构化快照和简短中文摘要。
6. 在同一事务中写快照、把通知改为 `consumed`、把任务改为 `awaiting_acceptance`，并写事件。

校验未通过时保留通知 `pending`，记录脱密错误和 `next_retry_at`。Worker 重启后继续扫描，不重复生成快照。

manager Provider 只负责把结构化字段压缩成摘要；Provider 不可用时使用确定性模板生成摘要，不能阻塞验收链路，也不能使用其他 Agent 的密钥。

## 5. 用户验收

管理员 API 提供：

- 交付快照列表和详情。
- 对当前待验收快照提交验收决定。
- 返工轮次和版本历史查询。

`accepted` 在一个事务中：

- 追加决定。
- 快照改为 `accepted`。
- 任务改为 `completed`。
- 写 `delivery.accepted` 和任务状态事件。

`rejected` 和 `revision_requested` 在一个事务中：

- 追加决定。
- 更新快照状态。
- 创建下一返工轮次和计划版本。
- 任务改为 `rework_planning`。
- 写返工事件。

决定意见最多 16 KiB。拒绝和要求调整必须填写意见；验收意见可为空。

## 6. 返工规划

返工 Worker 读取 `rework_rounds.status='planning'`，向 manager Provider 提供原任务、原计划、交付快照和用户意见。Provider 返回 M02 结构化规划格式。

系统复用 M02 的计划解析、依赖校验、Agent 匹配和工作区创建链路，但写入新的 `plan_version`。旧步骤保持 `completed`。新步骤从 `created` 开始，任务按现有流程进入 `planned`、`assigned` 和 `workspace_ready`。

Provider 配置缺失时返工轮次进入 `blocked: missing_config`，任务保持 `rework_planning`。配置恢复后 Worker 自动续跑。

## 7. 高风险边界

交付快照中的部署、数据删除、生产迁移、权限扩大和密钥操作标记为高风险。系统为每项创建阻塞 Issue，并在页面显示“需要单独确认”。

用户点击验收只确认当前代码交付，不授权高风险操作。管理员必须通过现有 Issue 流程单独确认；M08 不执行这些动作。

## 8. Web 页面

新增 `/deliveries` 交付验收页面，并从调度台和 MR 页面提供入口。页面包含：

- 待验收数量、已验收数量和返工轮次摘要。
- 交付版本列表、状态和项目 main commit。
- 工作包、Agent、MR、测试、风险、未完成项和用户决策证据。
- 历史决定和返工版本时间线。
- 验收、拒绝、要求调整按钮和意见输入。
- 高风险 Issue 提示，不提供部署或删除按钮。

页面使用管理员 API。按钮提交期间禁用，重复请求使用幂等键。移动端按版本列表、证据、决定区域顺序折叠。

## 9. 错误与并发

- 快照条件不满足：`409 delivery_not_ready`。
- 重复通知：返回已有快照，不创建新记录。
- 陈旧快照决定：`409 stale_snapshot`。
- 已验收任务再次决定：`409 acceptance_closed`。
- 拒绝缺少意见：`400 decision_comment_required`。
- 并发决定使用条件更新，只允许一个事务成功。
- Provider 故障记录脱密摘要并进入可恢复状态。
- API、日志、事件和快照禁止保存 Token、密钥、env 或完整 Provider 对话。

## 10. 防中断进度协议

M08 开始时创建 `feat/mission-08` 隔离 worktree。每个任务完成后执行：

1. 运行该任务的定向测试。
2. 更新 `wanxiangAgentWorkMission.md` 中的 `checkpoint_commit`、`completed`、`tests`、`blockers` 和唯一 `next_action`。
3. 中文提交并推送 `origin/feat/mission-08`。
4. 数据库 Worker 状态和 Git 提交必须互相印证。

恢复时按以下顺序读取：

1. `wanxiangAgent.md`。
2. `wanxiangAgentWorkMission.md` 的 M08 checkpoint。
3. 本规格和实施计划。
4. `origin/feat/mission-08` 最新提交。
5. 数据库中的快照、通知、返工轮次和计划版本状态。

任何 checkpoint 不一致时停止自动续跑，写 `blocked: checkpoint_mismatch`，等待 manager 或用户处理。

## 11. 测试与交付

自动化测试覆盖：

- 通知幂等消费和快照完整性。
- 未完成步骤、未合并 MR 和阻塞 Issue 拒绝生成快照。
- 验收完成任务；拒绝和调整创建新版本但不修改旧历史。
- 并发和重复决定。
- 返工 Provider 缺失、失败、恢复和新计划写入。
- 高风险事项只创建 Issue，不执行操作。
- API 身份、错误码和脱密。
- Web 列表、详情、决定、历史、空状态和移动端。
- Worker 或服务中断后从持久 checkpoint 恢复。

完成后运行 Go 全量测试、Web 全量测试和生产构建，扫描密钥，合并 `main`，更新生产 `web/dist` 和 Go 二进制，重启 PM2 并验证健康接口。
