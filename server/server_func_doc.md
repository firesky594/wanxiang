# Server 自定义函数索引

仅登记 `server/` 项目源码中可跨文件复用的导出函数和导出方法。Go 标准库、Chi、
SQLite 驱动等依赖方法，以及纯测试辅助、匿名回调和简单字段访问器不在此列。

1. `server/internal/agents/launcher.go` `NewLauncher`：创建并初始化 Agent 启动器。
2. `server/internal/agents/launcher.go` `Launcher.Start`：探测 Manager 并启动持续心跳。
3. `server/internal/agents/launcher.go` `Launcher.StartAll`：启动 Manager 及全部已配置 Agent。
4. `server/internal/agents/launcher.go` `Launcher.StartAgent`：探测并启动指定 Agent 的心跳。
5. `server/internal/agents/launcher.go` `Launcher.StartConfiguredAgent`：免探测恢复已在线 Agent 心跳。
6. `server/internal/agents/launcher.go` `Launcher.IsAgentActive`：判断 Agent 是否已加入心跳集合。
7. `server/internal/agents/launcher.go` `Launcher.Close`：停止巡检与全部 Agent 心跳。
8. `server/internal/agents/service.go` `NewService`：创建 Agent 配置与运行服务。
9. `server/internal/agents/service.go` `Service.EnsureManager`：初始化 Manager 目录并同步注册状态。
10. `server/internal/agents/service.go` `Service.ManagerReady`：确认 Manager 已初始化且在线。
11. `server/internal/agents/service.go` `Service.SaveManagerSecret`：安全写入 Manager 环境密钥。
12. `server/internal/agents/service.go` `Service.SaveAgentConfig`：校验并持久化 Agent 运行配置。
13. `server/internal/agents/service.go` `Service.GetAgentConfig`：读取指定 Agent 的脱敏运行配置。
14. `server/internal/agents/service.go` `Service.ListAgentConfigs`：列出全部 Agent 的脱敏配置与状态。
15. `server/internal/agents/service.go` `Service.ProbeAgent`：调用模型探测 Agent 并更新在线状态。
16. `server/internal/agents/service.go` `Service.ChatAgent`：按 Agent 配置调用模型对话。
17. `server/internal/agents/service.go` `Service.Heartbeat`：更新 Agent 心跳并发布心跳事件。
18. `server/internal/agents/service.go` `Service.RecordTokenUsage`：记录 Agent 模型用量并发布事件。
19. `server/internal/agents/service.go` `Service.WriteMemory`：安全写入 Agent 记忆目录文件。
20. `server/internal/agents/service.go` `Service.WriteLog`：安全写入 Agent 日志目录文件。
21. `server/internal/agents/service.go` `ValidateName`：校验 Agent 名称格式。
22. `server/internal/agents/supervisor.go` `NewManagerSupervisor`：创建总管确定性状态巡检器。
23. `server/internal/agents/supervisor.go` `ManagerSupervisor.Start`：启动总管周期状态巡检。
24. `server/internal/agents/supervisor.go` `ManagerSupervisor.Close`：停止巡检并等待扫描退出。
25. `server/internal/agents/supervisor.go` `ManagerSupervisor.Scan`：巡检项目、任务及 Agent 并写入记忆。
26. `server/internal/app/app.go` `New`：装配数据库、领域服务、Worker 与 HTTP 依赖。
27. `server/internal/app/app.go` `App.Close`：按依赖顺序停止 Worker 并关闭数据库。
28. `server/internal/assignments/admin.go` `Service.GetTaskMatch`：查询任务匹配结果、候选人与负责人信息。
29. `server/internal/assignments/admin.go` `Service.Override`：人工覆盖步骤的 Agent 分配结果。
30. `server/internal/assignments/service.go` `NewService`：创建任务分配服务。
31. `server/internal/assignments/service.go` `Service.AssignTask`：预检全部步骤后事务化匹配与分配 Agent。
32. `server/internal/assignments/worker.go` `NewWorker`：创建任务自动分配轮询器。
33. `server/internal/assignments/worker.go` `Worker.Start`：启动待分配任务轮询。
34. `server/internal/assignments/worker.go` `Worker.Close`：停止分配轮询并等待退出。
35. `server/internal/auth/auth.go` `HashSecret`：计算密钥的 SHA-256 标识。
36. `server/internal/auth/auth.go` `VerifySecret`：常量时间校验密钥哈希。
37. `server/internal/auth/auth.go` `HashPassword`：使用 PBKDF2 生成带盐密码哈希。
38. `server/internal/auth/auth.go` `VerifyPassword`：校验 PBKDF2 编码密码。
39. `server/internal/auth/auth.go` `PasswordNeedsRehash`：判断密码哈希参数是否需要升级。
40. `server/internal/config/config.go` `Load`：从根目录与环境变量加载服务配置。
41. `server/internal/db/db.go` `Open`：打开并配置 SQLite 数据库连接。
42. `server/internal/db/migrations.go` `Migrate`：幂等创建并升级数据库结构。
43. `server/internal/deliveries/acceptance.go` `Service.Decide`：记录交付验收决定并驱动后续状态。
44. `server/internal/deliveries/service.go` `NewService`：创建交付快照服务。
45. `server/internal/deliveries/service.go` `Service.BuildSnapshot`：汇总通知、合并请求与证据生成交付快照。
46. `server/internal/deliveries/service.go` `Service.List`：分页查询交付快照。
47. `server/internal/deliveries/service.go` `Service.Detail`：查询交付快照及关联明细。
48. `server/internal/deliveries/service.go` `Service.ListRework`：查询任务的返工轮次记录。
49. `server/internal/deliveries/worker.go` `NewWorker`：创建交付快照轮询器。
50. `server/internal/deliveries/worker.go` `Worker.Start`：启动交付通知扫描。
51. `server/internal/deliveries/worker.go` `Worker.Close`：停止交付扫描并等待退出。
52. `server/internal/deliveries/worker.go` `Worker.Scan`：处理待办通知并生成快照或触发返工。
53. `server/internal/events/bus.go` `NewBus`：创建数据库事件总线。
54. `server/internal/events/bus.go` `Bus.Publish`：持久化事件并广播给订阅者。
55. `server/internal/events/bus.go` `Bus.Notify`：向内存订阅者广播既有事件。
56. `server/internal/events/bus.go` `Bus.PublishJSON`：序列化载荷后发布领域事件。
57. `server/internal/events/bus.go` `Bus.Subscribe`：订阅实时事件并返回取消函数。
58. `server/internal/events/bus.go` `Bus.List`：分页查询持久化事件。
59. `server/internal/events/sse.go` `ServeSSE`：将事件订阅转换为 SSE 响应。
60. `server/internal/events/transaction.go` `InsertTx`：在现有事务内写入事件记录。
61. `server/internal/executor/admin.go` `NewAdminService`：创建执行器后台管理服务。
62. `server/internal/executor/admin.go` `AdminService.ListRuns`：查询任务的执行记录列表。
63. `server/internal/executor/admin.go` `AdminService.GetRun`：查询执行记录及动作明细。
64. `server/internal/executor/admin.go` `AdminService.Scan`：触发一次执行器任务扫描。
65. `server/internal/executor/admin.go` `AdminService.StopRun`：停止指定执行记录对应进程。
66. `server/internal/executor/bootstrap.go` `RunWorkerProcess`：装配并运行独立 Agent Worker 进程。
67. `server/internal/executor/checkpoint.go` `NewCheckpointRunner`：创建 Git 检查点执行器。
68. `server/internal/executor/checkpoint.go` `CheckpointRunner.CreateGitCheckpoint`：校验租约与工作区后创建 Git 检查点。
69. `server/internal/executor/checks.go` `NewCheckRunner`：创建命令检查执行器。
70. `server/internal/executor/checks.go` `CheckRunner.RunCheck`：校验租约后运行受限检查命令。
71. `server/internal/executor/files.go` `NewFileTools`：创建受租约保护的文件工具。
72. `server/internal/executor/files.go` `FileTools.ReadFile`：鉴权并读取工作区相对文件。
73. `server/internal/executor/files.go` `FileTools.WriteFile`：鉴权并原子写入工作区相对文件。
74. `server/internal/executor/protocol.go` `ParseProviderResponse`：严格解析并校验模型动作响应。
75. `server/internal/executor/redact.go` `Redact`：脱敏文本中的密钥与令牌。
76. `server/internal/executor/report.go` `NewDatabaseCompletionReporter`：创建受控数据库完成报告提交器。
77. `server/internal/executor/report.go` `DatabaseCompletionReporter.SubmitCompleted`：实时核验工作区后由干净检查点生成报告并送审。
78. `server/internal/executor/runner.go` `NewRunner`：创建可加载 Agent 提示词与记忆的动作执行器。
79. `server/internal/executor/runner.go` `Runner.SetCompletionReporter`：注入可选完成报告提交器。
80. `server/internal/executor/runner.go` `Runner.Run`：强制检查点并仅在报告送审成功后完成。
81. `server/internal/executor/supervisor.go` `NewSupervisor`：创建受并发限制的 Worker 监督器。
82. `server/internal/executor/supervisor.go` `Supervisor.Start`：启动执行任务轮询与进程监督。
83. `server/internal/executor/supervisor.go` `Supervisor.Scan`：扫描可执行租约并启动 Worker 进程。
84. `server/internal/executor/supervisor.go` `Supervisor.Close`：停止监督器及其全部活动进程。
85. `server/internal/executor/supervisor.go` `Supervisor.StopRun`：向指定活动执行进程发送停止信号。
86. `server/internal/executor/supervisor.go` `OSProcessLauncher.Launch`：按隔离参数启动 Agent Worker 子进程。
87. `server/internal/executor/supervisor.go` `osWorkerProcess.Wait`：等待 Worker 子进程退出。
88. `server/internal/executor/supervisor.go` `osWorkerProcess.Signal`：向 Worker 子进程发送终止信号。
89. `server/internal/executor/supervisor.go` `osWorkerProcess.Kill`：强制结束 Worker 子进程。
90. `server/internal/executor/supervisor.go` `limitedBuffer.Write`：写入经脱敏且有容量上限的输出缓冲。
91. `server/internal/executor/types.go` `RunStatus.Valid`：校验执行状态是否合法。
92. `server/internal/executor/types.go` `ActionType.Valid`：校验动作类型是否合法。
93. `server/internal/executor/worker.go` `RunWorker`：运行带租约心跳和中断检查点的工作循环。
94. `server/internal/executor/worker.go` `NewWorkerCommand`：创建仅传递白名单环境的 Worker 命令。
95. `server/internal/executor/worker.go` `NewEnvChatter`：创建基于进程环境配置的模型客户端。
96. `server/internal/executor/worker.go` `EnvChatter.ChatAgent`：使用进程环境配置调用模型对话。
97. `server/internal/executor/worker.go` `ProcessAgentEnv`：读取 Worker 允许使用的 Agent 环境变量。
98. `server/internal/files/safepath.go` `UnderRoot`：解析根目录内安全路径并拒绝链接逃逸。
99. `server/internal/gitx/git.go` `Run`：在指定仓库安全执行 Git 参数命令。
100. `server/internal/httpapi/middleware.go` `RequireAdmin`：创建管理员 Cookie 鉴权中间件。
101. `server/internal/httpapi/middleware.go` `RequireAgent`：创建 Agent Bearer 鉴权中间件。
102. `server/internal/httpapi/middleware.go` `AdminIdentity`：从请求上下文读取管理员身份。
103. `server/internal/httpapi/middleware.go` `AgentIdentity`：从请求上下文读取 Agent 名称。
104. `server/internal/httpapi/middleware.go` `AgentPrincipal`：从请求上下文读取 Agent 主体信息。
105. `server/internal/httpapi/router.go` `NewRouter`：注册并返回完整 HTTP API 路由。
106. `server/internal/issues/service.go` `NewService`：创建人工问题服务。
107. `server/internal/issues/service.go` `Service.Create`：创建问题并发布问题事件。
108. `server/internal/issues/service.go` `Service.HasBlockingForMR`：判断合并请求是否存在未解决阻塞问题。
109. `server/internal/issues/service.go` `Service.List`：分页查询问题列表。
110. `server/internal/leases/admin.go` `Service.ExtendResumeDeadline`：延长中断租约的恢复截止时间。
111. `server/internal/leases/admin.go` `Service.FreezeStep`：冻结步骤租约并阻塞任务步骤。
112. `server/internal/leases/admin.go` `Service.UnfreezeStep`：解冻步骤并签发新版活动租约。
113. `server/internal/leases/checkpoint.go` `Service.CreateCheckpoint`：校验租约与 Git 状态后持久化检查点。
114. `server/internal/leases/checkpoint.go` `Service.GetCheckpoint`：按编号查询检查点基础信息。
115. `server/internal/leases/clock.go` `SystemClock.Now`：返回当前 UTC 时间。
116. `server/internal/leases/guard.go` `Service.Authorize`：校验活动租约及工作区路径权限。
117. `server/internal/leases/handoff.go` `Service.Reassign`：基于检查点将中断步骤接管给新 Agent。
118. `server/internal/leases/recovery.go` `Service.InterruptExpired`：中断已过期租约并记录恢复窗口。
119. `server/internal/leases/recovery.go` `Service.Resume`：校验工作区现场并恢复中断租约。
120. `server/internal/leases/service.go` `NewService`：创建租约生命周期服务。
121. `server/internal/leases/service.go` `Service.Acquire`：为已分配步骤签发活动租约。
122. `server/internal/leases/service.go` `Service.Heartbeat`：续期活动租约并更新步骤心跳。
123. `server/internal/leases/timeline.go` `Service.GetCheckpointDetail`：查询检查点摘要、文件与测试明细。
124. `server/internal/leases/timeline.go` `Service.CurrentForAgent`：查询 Agent 当前步骤租约。
125. `server/internal/leases/timeline.go` `Service.Timeline`：汇总任务租约、检查点与接管时间线。
126. `server/internal/leases/types.go` `LeaseStatus.Valid`：校验租约状态是否合法。
127. `server/internal/leases/types.go` `Lease.PublicFor`：按查看者隐藏非本人租约凭据。
128. `server/internal/leases/worker.go` `NewWorker`：创建过期租约恢复轮询器。
129. `server/internal/leases/worker.go` `Worker.Start`：启动过期租约恢复轮询。
130. `server/internal/leases/worker.go` `Worker.Close`：停止租约轮询并等待退出。
131. `server/internal/leases/worker.go` `Worker.Run`：持续扫描并中断过期租约。
132. `server/internal/matching/definition.go` `LoadDefinition`：加载 Agent 定义、资源与记忆摘要。
133. `server/internal/matching/matcher.go` `Match`：按硬性条件与评分排序匹配 Agent。
134. `server/internal/mr/merge.go` `Service.Merge`：合并主线并事务完成步骤、租约及分配。
135. `server/internal/mr/merge.go` `Service.ReconcileMerge`：核对已外部合并提交并同步业务状态。
136. `server/internal/mr/query.go` `Service.Detail`：按主体权限查询合并请求详情。
137. `server/internal/mr/query.go` `Service.AdminList`：分页查询合并请求完整明细。
138. `server/internal/mr/query.go` `Service.AdminDetail`：后台查询单个合并请求完整明细。
139. `server/internal/mr/query.go` `Service.ListNotifications`：分页查询负责人交付通知。
140. `server/internal/mr/report.go` `Service.SubmitReport`：提交完成报告并事务切换步骤与分配为评审。
141. `server/internal/mr/review.go` `Service.Review`：负责人审批或退回合并请求。
142. `server/internal/mr/service.go` `NewService`：创建合并请求领域服务。
143. `server/internal/mr/service.go` `Service.Create`：创建基础合并请求并发布事件。
144. `server/internal/mr/service.go` `Service.List`：分页查询合并请求列表。
145. `server/internal/mr/service.go` `Service.ManagerMerge`：由 Manager 校验阻塞后合并主分支。
146. `server/internal/mr/service.go` `databaseBlockChecker.HasBlockingForMR`：查询数据库中的未解决阻塞问题。
147. `server/internal/mr/types.go` `CompletionReportInput.Validate`：校验完成报告身份、内容及大小限制。
148. `server/internal/mr/types.go` `ReviewInput.Validate`：校验评审身份、状态与内容限制。
149. `server/internal/pipelines/metadata.go` `LoadDefinition`：读取并校验项目流水线定义。
150. `server/internal/pipelines/metadata.go` `Validate`：校验流水线步骤、参数与发布约束。
151. `server/internal/pipelines/runner.go` `CommandRunner.Run`：在受限环境中执行白名单流水线命令。
152. `server/internal/pipelines/service.go` `NewService`：创建流水线服务。
153. `server/internal/pipelines/service.go` `Service.Start`：幂等创建流水线运行及步骤。
154. `server/internal/pipelines/service.go` `Service.Confirm`：人工确认待执行的高风险步骤。
155. `server/internal/pipelines/service.go` `Service.ConfirmRollback`：人工确认待执行的流水线回滚。
156. `server/internal/pipelines/service.go` `Service.Get`：查询流水线运行及全部步骤。
157. `server/internal/pipelines/service.go` `Service.List`：查询最近的流水线运行列表。
158. `server/internal/pipelines/worker.go` `NewWorker`：创建流水线执行轮询器。
159. `server/internal/pipelines/worker.go` `Worker.Start`：启动流水线步骤与回滚轮询。
160. `server/internal/pipelines/worker.go` `Worker.Close`：停止流水线轮询并等待退出。
161. `server/internal/pipelines/worker.go` `Worker.Scan`：恢复中断步骤并执行可运行流水线任务。
162. `server/internal/planning/prompt.go` `BuildMessages`：结合总管提示词、记忆与库存组装规划消息。
163. `server/internal/planning/service.go` `NewService`：创建任务规划服务。
164. `server/internal/planning/service.go` `Service.PlanTask`：调用 Manager 生成并持久化初版计划。
165. `server/internal/planning/service.go` `Service.PlanRework`：结合验收反馈生成并持久化返工计划。
166. `server/internal/planning/validate.go` `ParsePlan`：严格解析并校验结构化任务计划。
167. `server/internal/planning/worker.go` `NewWorker`：创建任务规划轮询器。
168. `server/internal/planning/worker.go` `Worker.Start`：启动待规划任务轮询。
169. `server/internal/planning/worker.go` `Worker.Close`：停止规划轮询并等待退出。
170. `server/internal/providers/deepseek.go` `NewDeepSeek`：创建 DeepSeek 模型客户端。
171. `server/internal/providers/deepseek.go` `DeepSeek.Chat`：调用 DeepSeek 对话接口并解析用量。
172. `server/internal/providers/openai.go` `NewOpenAI`：创建 OpenAI 模型客户端。
173. `server/internal/providers/openai.go` `OpenAI.Chat`：调用 OpenAI 对话接口并解析用量。
174. `server/internal/providers/types.go` `NewRegistry`：创建并注册受支持模型提供商。
175. `server/internal/providers/types.go` `Registry.Get`：按类型取得模型提供商实现。
176. `server/internal/providers/types.go` `DefaultBaseURL`：返回模型提供商默认接口地址。
177. `server/internal/tasks/service.go` `Service.List`：分页查询任务列表。
178. `server/internal/tasks/service.go` `Service.ListProjects`：分页查询项目列表。
179. `server/internal/tasks/service.go` `Service.Get`：查询任务、项目、步骤与依赖详情。
180. `server/internal/tasks/service.go` `Service.UpdateStatus`：校验状态机后更新任务状态并发布事件。
181. `server/internal/tasks/service.go` `NewService`：创建任务领域服务。
182. `server/internal/tasks/service.go` `Service.CreateTask`：按标题与描述创建新项目任务。
183. `server/internal/tasks/service.go` `Service.CreateTaskWithInput`：按幂等键事务化创建任务及领域事件。
184. `server/internal/workspaces/metadata.go` `EncodeProject`：校验并编码项目元数据 YAML。
185. `server/internal/workspaces/metadata.go` `EncodeAssignment`：校验并编码分配元数据及摘要。
186. `server/internal/workspaces/metadata.go` `DecodeAssignment`：解析并校验分配元数据 YAML。
187. `server/internal/workspaces/ownership.go` `Service.AuthorizeAgent`：校验 Agent 对相对路径的写入范围。
188. `server/internal/workspaces/reconcile.go` `Service.ReconcileTask`：核对数据库、快照与 Worktree 漂移。
189. `server/internal/workspaces/reconcile.go` `Service.RepairTask`：按指定可信源修复工作区漂移。
190. `server/internal/workspaces/reconcile.go` `Service.RequestCleanup`：校验条件并登记工作区清理请求。
191. `server/internal/workspaces/reconcile.go` `Service.ConfirmCleanup`：复核现场后移除任务 Worktree。
192. `server/internal/workspaces/service.go` `NewService`：创建任务工作区服务。
193. `server/internal/workspaces/service.go` `Service.ProvisionTask`：创建分支、Worktree 与所有权元数据。
194. `server/internal/workspaces/service.go` `Service.GetTask`：查询任务工作区及各步骤状态。
195. `server/internal/workspaces/worker.go` `NewWorker`：创建工作区装配与校准轮询器。
196. `server/internal/workspaces/worker.go` `Worker.Start`：启动工作区装配与校准轮询。
197. `server/internal/workspaces/worker.go` `Worker.Close`：停止工作区轮询并等待退出。
