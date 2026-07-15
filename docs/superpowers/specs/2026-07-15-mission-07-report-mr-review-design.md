# M07 完成报告、MR 与项目负责人审核设计

日期：2026-07-15
状态：已确认设计，等待书面规格复核

## 1. 目标

M07 补齐执行 Agent 从完成工作到项目 `main` 合并的闭环：执行 Agent 原子提交结构化完成报告和 MR，项目负责人审核、退回或合并，合并成功后服务向 manager 写入结构化通知。

客户端不参与代码生命周期。客户端只提交 Issue、补充需求、确认高风险操作和查看状态，不创建完成报告、MR、审核记录或合并操作。

## 2. 范围

M07 实现以下能力：

- 完成报告与 MR 原子创建。
- Token、`agent_name`、`role`、任务、步骤和租约的交叉校验。
- 项目负责人审核、退回、批准和合并。
- 单 Agent 项目由该 Agent 以项目负责人身份自审自合并。
- 多 Agent 项目由登记的项目负责人按依赖顺序合并。
- 合并前校验 checkpoint、HEAD、分支归属、阻塞 Issue、依赖和工作区状态。
- 合并冲突时执行 `git merge --abort`，不改变 MR 的已审核事实。
- 合并成功后写入 manager 通知和运行事件。
- 管理台移除创建 MR、填写 `created_by` 和触发 Agent 合并的入口，只保留查询视图。

M07 不负责 manager 面向用户的最终验收、返工编排、自动发布或回滚。这些能力属于 M08 及后续 Mission。

## 3. 身份与权限

### 3.1 请求身份

所有 Agent 写请求必须同时携带：

- Agent Token。
- `agent_name`。
- `role`。
- `task_id`、`step_id` 和 `lease_id`。

服务端以 Token 解析出的身份为根，交叉校验 Agent 注册表角色、任务分配、当前有效租约、项目成员关系、`project_lead` 和分支归属。请求体声明不能覆盖服务端身份或权限。

### 3.2 权限边界

- 执行 Agent 只能为自己领取的步骤和自己拥有的分支提交完成报告与 MR。
- 普通执行 Agent 不能审核、合并项目 `main`，也不能操作其他 Agent 的分支或 worktree。
- 项目负责人只能审核和合并自己负责项目内的 MR。
- 单 Agent 项目中，同一 Agent 同时具备执行者和项目负责人权限。服务端仍执行全部报告、租约、checkpoint、分支和合并校验。
- manager 只在负责人失联、租约撤销或用户授权时接管。接管原因必须进入事件和审计记录。
- 管理员会话和客户端请求没有 Agent 写权限。

## 4. 数据模型

### 4.1 `completion_reports`

每条记录对应一次不可变的报告版本，包含：

- `id`、`version`、`created_at`。
- `project_id`、`task_id`、`step_id`、`lease_id`。
- `agent_name`、提交时服务端确认的 `agent_role`。
- `source_branch`、`checkpoint_commit`、`head_commit`。
- 已完成事项、未完成事项、关键文件、测试证据、风险、依赖和建议合并顺序。
- 是否需要用户决策及决策说明。

结构化列表使用 JSON 字段保存，服务层负责字段数量、字符串长度和合法值校验。负责人退回后，执行 Agent 创建新版本；旧版本保留，不能原地覆盖。

### 4.2 `merge_requests`

扩展现有表，增加：

- `report_id`、`step_id`、`lease_id`、`report_version`。
- `source_commit`、`project_lead`。
- `reviewed_at`、`approved_at`、`merged_by`、`merge_commit`。

状态限定为：

- `pending_review`：等待项目负责人审核。
- `changes_requested`：负责人要求修改，当前版本不能合并。
- `approved`：审核通过，等待执行合并。
- `merged`：已合入项目 `main`。
- `closed`：取消或由新 MR 替代。

一个报告版本只能创建一个 MR。数据库唯一约束阻止重复提交。

### 4.3 `mr_reviews`

审核记录包含 MR、报告版本、审核者 Agent、服务端确认的角色、结论、意见和时间。结论限定为 `changes_requested` 或 `approved`。每次审核追加记录，不覆盖历史。

### 4.4 `manager_notifications`

项目负责人合并后写入：

- 项目、任务、MR、报告和负责人身份。
- 项目 `main` 的合并提交。
- 已合并分支和 MR 列表。
- 测试摘要、风险、未完成事项和用户决策项。
- 通知状态与创建时间。

manager 后续按通知状态消费。M07 只负责可靠写入和查询，不负责生成面向用户的最终验收文本。

## 5. 接口

### 5.1 执行 Agent

`POST /api/agent/completion-reports`

服务在一个数据库事务中：

1. 校验身份、角色、任务、步骤、租约和分支归属。
2. 校验 checkpoint、HEAD 和报告内容。
3. 创建完成报告版本。
4. 创建唯一 MR，状态为 `pending_review`。
5. 在同一事务中写入持久化的 `report.created` 和 `mr.created` 事件。

任一步失败时不保留报告或 MR。`created_by`、负责人和角色均由服务端填写。

### 5.2 项目负责人

- `GET /api/agent/mrs/{id}`：读取 MR、报告、审核历史和合并条件。
- `POST /api/agent/mrs/{id}/reviews`：提交 `approved` 或 `changes_requested`。
- `POST /api/agent/mrs/{id}/merge`：合并已批准的 MR。

审核和合并接口复用相同的负责人权限守卫。单 Agent 项目不跳过审核步骤；该 Agent 先写批准记录，再调用合并接口。

### 5.3 管理员与客户端

管理员 API 只提供完成报告、MR、审核和 manager 通知的查询。管理台删除 MR 创建、`created_by` 输入和合并按钮，避免管理员会话误用 Agent API。

## 6. 原子提交与状态流转

完成报告、MR 和对应的 `runtime_events` 使用同一事务创建。事务提交后只触发 SSE 唤醒；即使即时推送失败，客户端刷新或重连仍能读取持久事件，服务不能重复创建业务记录。

状态流转如下：

```text
pending_review -> changes_requested -> closed
                              \-> 新报告版本 + 新 MR
pending_review -> approved -> merged
approved -> changes_requested
```

执行 Agent 为 `changes_requested` 的 MR 提交新报告版本时，服务在同一事务中关闭旧 MR 并创建新 MR。负责人发现新风险时可以把 `approved` 退回 `changes_requested`，但合并开始后不能再修改审核状态。服务以条件更新防止并发审核或重复合并。

## 7. 合并校验与 Git 操作

合并接口依次执行：

1. 校验调用者是当前项目负责人，或具备已记录原因的 manager 接管权限。
2. 校验 MR 为 `approved`，报告版本与 MR 一致。
3. 校验步骤分配、租约、checkpoint 和 `source_commit` 未漂移。
4. 校验依赖步骤和依赖 MR 已完成或已合并。
5. 校验没有未解决的阻塞 Issue。
6. 校验源分支属于报告 Agent，项目仓库和目标 worktree 干净。
7. checkout 项目 `main`，执行 `git merge --no-ff --no-edit <source_branch>`。
8. 冲突时执行 `git merge --abort`，记录失败事件，MR 保持 `approved`。
9. 成功后读取 merge commit，在同一数据库事务中更新 MR、步骤状态，写 manager 通知和事件。

Git 已成功但数据库提交失败属于需人工恢复的高风险状态。服务写入独立恢复事件并阻止重复 Git 合并；后续实现必须提供按 `source_commit` 和实际 `main` 祖先关系对账的恢复路径。

## 8. 错误处理

- 身份、角色或负责人声明不一致：返回 `403 identity_mismatch` 并写审计事件。
- 租约过期或不属于调用者：返回 `409 lease_invalid`，进入 M05 恢复流程。
- checkpoint 或 HEAD 漂移：返回 `409 checkpoint_mismatch`，要求重新生成报告版本。
- 依赖未完成：返回 `409 dependency_not_ready`。
- 阻塞 Issue 未解决：返回 `409 blocking_issue`。
- 工作区不干净：返回 `409 dirty_worktree`。
- Git 冲突：返回 `409 merge_conflict`，确认 abort 后保留 `approved` 状态。
- 配置或数据库故障：返回脱密错误，禁止把 env、Token、密钥或 Provider 原始认证信息写入响应、报告和事件。

## 9. 测试与验收

后端测试覆盖：

- 报告与 MR 原子创建，重复报告版本被拒绝。
- 客户端或管理员会话不能调用 Agent 写接口。
- `created_by`、`agent_name` 或 `role` 伪造被拒绝。
- 单 Agent 作为项目负责人完成自审、自合并和 manager 通知。
- 多 Agent 项目中普通执行 Agent 不能审核或合并。
- 非本项目负责人、过期租约和错误分支被拒绝。
- checkpoint、HEAD、依赖、阻塞 Issue 和干净工作区校验。
- 合并冲突完成 abort，MR 状态可重试。
- 合并成功保存 merge commit，并原子写入 manager 通知。
- 日志、事件、报告和 API 响应不包含测试密钥。

前端测试覆盖管理台只能查询报告、MR 和审核结果，不再发起 Agent 写操作。前端修改后执行 `npm` 测试和 `dist` 构建；后端修改后执行 Go 全量测试、替换生产二进制并通过 PM2 重启 `wanxiang-agent`，再用绕过代理的健康检查验证。

## 10. 交付边界

M07 完成时，项目分支合并链路以 manager 通知为终点。M08 从通知读取项目结果，生成用户验收摘要，并处理返工。这样可避免 M07 同时承担代码审核和业务验收两种职责。
