# Wanxiang Agent 当前 Mission

## R001 后台导航与 Tab 工作区

### Mission 状态

```yaml
requirement_id: R001
status: 正在开发
stage: 设计已确认，等待用户审阅 Mission
frontend_build_required: true
frontend_build_command: npm test -- --run && npm run build
frontend_build_result: not_run
frontend_dist_path: web/dist
frontend_deployed: false
backend_build_required: false
backend_build_command: not_required
backend_build_result: not_required
backend_restart_required: false
backend_restarted: false
backend_restart_reason: 本 Mission 只调整 Vue 管理台，不修改 Go 后端或 API 契约
backend_process_manager: pm2
backend_pm2_app: wanxiang-agent
backend_pm2_status: not_required
backend_healthcheck_result: not_checked
```

### 目标

将现有页面顶部导航改为统一后台壳层：左侧放置可折叠的完整导航，右侧顶部放置已打开页面的 Tab 标签，Tab 下方展示当前页面内容。

### 已确认的交互

1. 首次进入后台时没有已打开 Tab，右侧直接显示 Dashboard 内容。
2. 点击左侧导航后创建并激活对应 Tab；重复点击只激活已有 Tab。
3. 所有 Tab 都允许关闭。
4. 关闭当前 Tab 后切换到相邻 Tab；关闭到零个 Tab 时恢复无标签的 Dashboard。
5. 左侧导航折叠时只显示图标，展开后同时显示图标和导航文字。
6. 已打开 Tab、当前激活 Tab 和侧栏折叠状态保存到浏览器本地，刷新后恢复。
7. 登录和初始化页面不显示后台壳层。
8. 窄屏下左侧导航使用抽屉式展示，不持续挤压内容区。

### 设计

采用统一后台壳层，不在各业务页面中分别维护导航：

- `App.vue` 根据路由是否公开决定展示登录页面或后台壳层。
- 新增后台布局组件，统一负责侧栏、折叠按钮、Tab 栏和路由内容出口。
- 新增 Pinia Tab 状态仓库，管理打开、激活、关闭、恢复和本地持久化。
- 路由元数据提供导航标题、图标标识和是否进入后台 Tab。
- 已打开页面通过 Vue `KeepAlive` 保留临时表单、筛选和滚动相关组件状态。
- 各业务页面删除重复的 `.topbar`，只保留页面主体。

Tab 状态只保存在当前浏览器的 `localStorage`，不写入后端数据库，也不跨浏览器同步。恢复时只接受当前路由表中仍存在的后台页面，忽略失效或未知路径。

### 范围

本 Mission 包含：

- 后台统一左右布局。
- 左侧导航展开、折叠和窄屏抽屉。
- 右侧 Tab 的打开、切换、关闭与刷新恢复。
- 现有后台页面接入统一布局。
- 相关组件、状态仓库和路由行为测试。

本 Mission 不包含：

- R002 的 Agents 列表及右侧配置详情重构。
- 后端接口、数据库或权限模型调整。
- 跨账号、跨浏览器的 Tab 同步。
- 拖拽排序、固定 Tab、右键菜单或多窗口。

### 验收条件

1. 初次进入 `/dashboard` 时 Tab 栏为空并显示 Dashboard。
2. 左侧每个现有后台导航均能打开唯一 Tab，并正确显示对应页面。
3. Tab 切换同步浏览器地址，刷新后恢复打开项和当前项。
4. 任意 Tab 均可关闭；零 Tab 时返回无标签 Dashboard。
5. 左栏折叠时只显示图标，展开后显示图标和文字。
6. 登录和初始化路由不出现后台侧栏与 Tab。
7. 桌面端和窄屏布局均可操作，键盘焦点和导航标签可识别。
8. `npm test -- --run` 与 `npm run build` 通过。
9. 新 `web/dist` 已替换生产静态资源，并完成页面访问健康检查后，R001 才能归档为“已完成”。

### 当前证据

- 当前各业务页面分别重复渲染 `.topbar` 和 `.nav`。
- `web/src/App.vue` 当前只有顶层 `<router-view />`，尚无统一后台布局。
- `web/src/router.ts` 已集中登记 Dashboard、Agents、Task、MR、Issue、交付验收和流水线路由。
- `web/src/style.css` 已有全局控制台样式，可作为新壳层的基础样式来源。
- 2026-07-24 用户确认采用可折叠双态侧栏，并确认刷新后保留 Tab。

### 下一步

用户审阅并确认本 Mission 后：

1. 基于本 Mission 编写逐步实施计划。
2. 先增加失败测试，覆盖初始 Dashboard、Tab 打开/关闭/恢复和侧栏双态。
3. 实现最小后台壳层和 Tab 状态。
4. 迁移现有后台页面，执行前端测试与构建。
5. 部署 `web/dist` 并执行生产页面健康检查。
6. 更新 R001 状态与 Mission 总结，清理本文件中的已完成 Mission。
