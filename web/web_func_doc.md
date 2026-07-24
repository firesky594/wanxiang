# Web 自定义函数索引

仅登记 `web/src` 内项目自行定义的可调用函数；Vue、Pinia、Vue Router、Element Plus、
Vue Flow、浏览器 API 及框架生命周期回调不在此列。

1. `web/src/api/client.ts` `api`：统一发送鉴权请求并处理管理员登录失效。
2. `web/src/api/client.ts` `listAgentConfigs`：获取全部 Agent 模型配置。
3. `web/src/api/client.ts` `saveAgentConfig`：保存指定 Agent 的模型配置。
4. `web/src/api/client.ts` `probeAgent`：探测指定 Agent 模型接口的可用性。
5. `web/src/api/client.ts` `createAdminTask`：创建后台任务并可选择复用已有项目。
6. `web/src/api/client.ts` `workspaceAction`：统一提交任务工作区管理操作。
7. `web/src/api/client.ts` `getTaskWorkspace`：获取指定任务的工作区快照。
8. `web/src/api/client.ts` `reconcileTaskWorkspace`：校验并协调任务工作区与记录状态。
9. `web/src/api/client.ts` `repairTaskWorkspace`：按指定数据源修复任务工作区漂移。
10. `web/src/api/client.ts` `cleanupTaskWorkspace`：申请或确认清理任务工作区。
11. `web/src/api/client.ts` `getLeaseTimeline`：获取任务租约、检查点与接管时间线。
12. `web/src/api/client.ts` `leaseAdminAction`：统一提交步骤租约管理操作。
13. `web/src/api/client.ts` `extendLeaseDeadline`：延长指定步骤租约的恢复期限。
14. `web/src/api/client.ts` `freezeLease`：冻结指定步骤租约并撤销写权限。
15. `web/src/api/client.ts` `unfreezeLease`：解冻指定步骤并换发新租约。
16. `web/src/api/client.ts` `reassignLease`：将指定步骤交给新的 Agent。
17. `web/src/api/client.ts` `getTaskMatch`：获取任务步骤的 Agent 匹配结果。
18. `web/src/api/client.ts` `overrideTaskMatch`：人工改写步骤的 Agent 匹配结果。
19. `web/src/api/client.ts` `listMergeRequests`：获取合并请求列表及交付报告。
20. `web/src/api/client.ts` `getMergeRequest`：获取单个合并请求的完整详情。
21. `web/src/api/client.ts` `listDeliveries`：获取全部任务交付快照。
22. `web/src/api/client.ts` `getDelivery`：获取指定交付快照及决策详情。
23. `web/src/api/client.ts` `decideDelivery`：提交交付验收、拒绝或返工决定。
24. `web/src/api/client.ts` `listPipelines`：获取全部流水线运行记录。
25. `web/src/api/client.ts` `getPipeline`：获取指定流水线运行详情。
26. `web/src/api/client.ts` `startPipeline`：为项目启动一条新的流水线。
27. `web/src/api/client.ts` `confirmPipeline`：确认执行流水线中的高风险步骤。
28. `web/src/api/client.ts` `confirmPipelineRollback`：确认回滚指定流水线运行。
29. `web/src/stores/auth.ts` `saveToken`：保存管理员令牌到状态和本地存储。
30. `web/src/stores/auth.ts` `login`：校验管理员账号并保存登录令牌。
31. `web/src/stores/auth.ts` `bootstrap`：初始化首个管理员账号并保存令牌。
32. `web/src/stores/auth.ts` `loadManager`：加载 Manager 状态及缺失配置。
33. `web/src/stores/auth.ts` `saveManagerSecret`：保存 Manager 密钥并刷新状态。
34. `web/src/stores/events.ts` `hydrate`：合并历史事件、过滤心跳并排序。
35. `web/src/stores/events.ts` `connect`：建立 SSE 连接并注册事件监听。
36. `web/src/stores/events.ts` `pushEvent`：解析、过滤、去重并追加实时事件。
37. `web/src/stores/tasks.ts` `loadList`：加载后台任务列表并维护请求状态。
38. `web/src/stores/tasks.ts` `loadDetail`：加载指定任务详情并维护请求状态。
39. `web/src/stores/workspaceTabs.ts` `isWorkspaceTab`：判断未知值是否为合法工作区标签。
40. `web/src/stores/workspaceTabs.ts` `persist`：持久化标签页和侧栏界面状态。
41. `web/src/stores/workspaceTabs.ts` `openTab`：打开并激活指定工作区标签。
42. `web/src/stores/workspaceTabs.ts` `activateTab`：激活已经打开的工作区标签。
43. `web/src/stores/workspaceTabs.ts` `closeTab`：关闭标签并选择相邻激活标签。
44. `web/src/stores/workspaceTabs.ts` `setSidebarCollapsed`：更新并保存侧栏折叠状态。
45. `web/src/stores/workspaceTabs.ts` `restore`：校验允许路径并恢复工作区状态。
46. `web/src/components/AdminShell.vue` `routeTitle`：生成当前路由对应的标签页标题。
47. `web/src/components/AdminShell.vue` `syncRouteToTabs`：将当前工作区路由同步为标签页。
48. `web/src/components/AdminShell.vue` `isWorkspacePath`：判断路径是否属于工作区页面。
49. `web/src/components/AdminShell.vue` `openNavigation`：打开导航标签并跳转对应路由。
50. `web/src/components/AdminShell.vue` `activateTab`：激活标签并跳转到对应路由。
51. `web/src/components/AdminShell.vue` `closeTab`：关闭标签并切换相邻页或 Dashboard。
52. `web/src/components/AgentOutputPanel.vue` `formatPayload`：将事件载荷格式化为 JSON 文本。
53. `web/src/views/AdminAccess.vue` `submit`：校验凭证并登录或初始化管理员。
54. `web/src/views/Agents.vue` `load`：加载 Agent 配置列表并维护状态。
55. `web/src/views/Agents.vue` `edit`：将选中 Agent 配置回填到表单。
56. `web/src/views/Agents.vue` `applyProviderDefault`：应用服务商默认地址和模型。
57. `web/src/views/Agents.vue` `resetForm`：清除选择并恢复 Agent 表单默认值。
58. `web/src/views/Agents.vue` `save`：校验并保存 Agent 配置后刷新列表。
59. `web/src/views/Agents.vue` `probe`：探测 Agent 接口并刷新配置状态。
60. `web/src/views/Dashboard.vue` `createTask`：校验信息并按项目模式创建任务。
61. `web/src/views/Deliveries.vue` `label`：将交付状态转换为中文名称。
62. `web/src/views/Deliveries.vue` `short`：截取提交标识用于简洁展示。
63. `web/src/views/Deliveries.vue` `load`：加载交付列表及当前交付详情。
64. `web/src/views/Deliveries.vue` `select`：加载交付详情并重置验收输入。
65. `web/src/views/Deliveries.vue` `submitDecision`：校验并提交交付验收决定。
66. `web/src/views/Issues.vue` `createIssue`：整理表单参数并创建人工问题记录。
67. `web/src/views/MergeRequests.vue` `statusLabel`：将合并请求状态转换为中文名称。
68. `web/src/views/MergeRequests.vue` `shortCommit`：截取提交哈希用于简洁展示。
69. `web/src/views/MergeRequests.vue` `loadMergeRequests`：加载合并请求列表及当前详情。
70. `web/src/views/MergeRequests.vue` `selectMR`：加载指定合并请求的完整详情。
71. `web/src/views/Pipelines.vue` `load`：加载流水线运行记录并维护状态。
72. `web/src/views/Pipelines.vue` `confirm`：二次确认并授权执行流水线步骤。
73. `web/src/views/Pipelines.vue` `rollback`：二次确认并提交生产回滚授权。
74. `web/src/views/TaskDetail.vue` `override`：为指定任务步骤人工改派 Agent。
75. `web/src/views/TaskDetail.vue` `shortCommit`：截取工作区提交哈希用于展示。
76. `web/src/views/TaskDetail.vue` `runWorkspace`：统一执行工作区操作并维护状态。
77. `web/src/views/TaskDetail.vue` `reconcileWorkspace`：校验工作区与登记状态是否一致。
78. `web/src/views/TaskDetail.vue` `repairWorkspace`：按指定基准修复工作区漂移。
79. `web/src/views/TaskDetail.vue` `requestCleanup`：申请将任务工作区标记为待清理。
80. `web/src/views/TaskDetail.vue` `confirmCleanup`：清理已验证归属的任务工作树。
81. `web/src/views/TaskDetail.vue` `leaseFor`：查找指定步骤对应的当前租约。
82. `web/src/views/TaskDetail.vue` `checkpointFor`：查找指定步骤对应的恢复检查点。
83. `web/src/views/TaskDetail.vue` `formatTime`：将时间值格式化为本地可读文本。
84. `web/src/views/TaskDetail.vue` `remaining`：计算并展示恢复期限剩余秒数。
85. `web/src/views/TaskDetail.vue` `loadRecovery`：加载任务租约与恢复时间线。
86. `web/src/views/TaskDetail.vue` `extendRecovery`：确认并延长步骤恢复期限。
87. `web/src/views/TaskDetail.vue` `freezeRecovery`：确认并冻结工作包及写权限。
88. `web/src/views/TaskDetail.vue` `unfreezeRecovery`：确认解冻工作包并换发租约。
89. `web/src/views/TaskDetail.vue` `reassignRecovery`：校验风险并创建步骤接力工作区。
90. `web/src/components/AgentCanvas.vue` `isConnectedStatus`：判断 Agent 状态是否表示当前可连接。
91. `web/src/components/AgentCanvas.vue` `formatAgentStatus`：将 Agent 原始状态转换为画布文字。
92. `web/src/components/AgentCanvas.vue` `agentTone`：按 Agent 名称稳定选择节点强调色。
93. `web/src/components/AgentCanvas.vue` `readLayout`：读取并校验已保存的 Agent 画布位置。
94. `web/src/components/AgentCanvas.vue` `defaultPosition`：为 Agent 生成不重叠的默认坐标。
95. `web/src/components/AgentCanvas.vue` `syncAgentNodes`：按最新 Agent 数据同步节点及位置。
96. `web/src/components/AgentCanvas.vue` `persistNodePosition`：保存拖动结束后的 Agent 节点坐标。
97. `web/src/components/AgentCanvas.vue` `resetLayout`：清除自定义坐标并恢复默认布局。
98. `web/src/views/Dashboard.vue` `refreshAgents`：刷新 Agent 列表、连接状态及错误信息。
99. `web/src/views/Dashboard.vue` `loadDashboardData`：初始化任务、项目、Agent 与实时事件。
100. `web/src/views/Dashboard.vue` `openTaskComposer`：打开新建任务抽屉并清除上次结果。
101. `web/src/views/Dashboard.vue` `openTaskList`：打开并刷新持久任务列表抽屉。
102. `web/src/views/Dashboard.vue` `resetAgentLayout`：重置 Agent 画布布局并提示结果。
103. `web/src/components/AgentCanvas.vue` `selectAgent`：向父级发送用户选择的 Agent。
104. `web/src/components/AgentCanvas.vue` `selectAgentFromNode`：将画布节点点击转换为 Agent 选择。
105. `web/src/components/AgentConfigPanel.vue` `isAgentConnected`：判断 Agent 是否处于已连接状态。
106. `web/src/components/AgentConfigPanel.vue` `hydrateForm`：用脱敏配置初始化 Agent 编辑表单。
107. `web/src/components/AgentConfigPanel.vue` `applyProviderDefault`：按 Provider 应用默认模型和地址。
108. `web/src/components/AgentConfigPanel.vue` `save`：保存 Agent 配置并执行真实接口探测。
109. `web/src/components/AgentConfigPanel.vue` `probe`：重新探测 Agent 接口并刷新状态。
110. `web/src/views/Dashboard.vue` `syncDrawerSize`：同步 Element Plus 可解析的抽屉宽度。
111. `web/src/views/Dashboard.vue` `openAgentConfig`：打开所选 Agent 的配置面板。
112. `web/src/views/Dashboard.vue` `handleAgentConfigUpdated`：配置操作后刷新 Agent 状态。
113. `web/src/views/Dashboard.vue` `handleDrawerClosed`：抽屉关闭后清除 Agent 选择。
