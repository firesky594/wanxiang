# Wanxiang Agent 当前 Mission

## R001 后台导航与 Tab 工作区

### Mission 状态

```yaml
requirement_id: R001
status: 正在开发
stage: 实施计划已建立，准备按 TDD 执行
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

### 执行前运行基线

```yaml
checked_at: 2026-07-24T09:51:40+08:00
backend_process_manager: pm2
backend_pm2_app: wanxiang-agent
backend_pm2_status: online
backend_listen_address: 127.0.0.1:8088
backend_healthcheck_command: curl --noproxy '*' http://127.0.0.1:8088/api/health
backend_healthcheck_result: '{"ok":true}'
development_runtime_rule: 开发与测试期间保持现有 PM2 服务在线，不启动第二份后端
```

首次健康检查紧跟在 `pm2 start` 后执行，发生连接拒绝；PM2 日志随后记录
`wanxiang-agent listening addr=127.0.0.1:8088`，同一进程稳定为 `online` 后健康检查通过。
根因是启动与监听之间的短暂时序窗口，不是进程启动失败。后续校验先确认监听状态，再请求健康接口。

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

执行下面的实施计划。每个任务严格遵循“失败测试 → 最小实现 → 测试通过 → 中文提交”，
不修改或提交当前工作区内与 R001 无关的历史文档删除。

### 实施计划

> **执行方式：** 当前会话逐项执行；每个任务完成后检查差异和运行状态。
>
> **目标：** 建立左侧可折叠导航、右侧持久化 Tab 和路由内容区组成的统一后台壳层。
>
> **技术栈：** Vue 3.5、Vue Router 4.6、Pinia 3、Element Plus 2.11、Vitest 3。

#### 任务 1：Tab 状态和本地恢复

**文件**

- 新建：`web/src/stores/workspaceTabs.ts`
- 新建：`web/src/stores/workspaceTabs.test.ts`

**接口**

```ts
export interface WorkspaceTab {
  path: string
  title: string
}

export const useWorkspaceTabsStore = defineStore('workspaceTabs', {
  state: () => ({
    tabs: [] as WorkspaceTab[],
    activePath: '',
    sidebarCollapsed: true
  })
})
```

Store 提供 `openTab(tab)`、`activateTab(path)`、`closeTab(path)`、`setSidebarCollapsed(value)`
和 `restore()`。持久化键固定为 `wanxiang_workspace_v1`；写入内容只包含
`tabs`、`activePath` 和 `sidebarCollapsed`。`restore()` 接收路由允许路径集合并过滤未知路径。

- [ ] 先写测试：初始状态没有 Tab，侧栏默认折叠。
- [ ] 运行 `npm test -- --run src/stores/workspaceTabs.test.ts`，确认因 Store 不存在而失败。
- [ ] 实现打开去重、激活、关闭当前后选择右邻或左邻、零 Tab 时清空 `activePath`。
- [ ] 增加持久化恢复测试，确认未知路径被丢弃、有效 Tab 和折叠状态被恢复。
- [ ] 重跑单测并确认通过。
- [ ] 提交 `功能：增加后台标签状态管理`。

#### 任务 2：路由元数据和统一后台壳层

**文件**

- 修改：`web/src/router.ts`
- 新建：`web/src/components/AdminShell.vue`
- 新建：`web/src/components/AdminShell.test.ts`

**路由元数据**

```ts
interface RouteMeta {
  public?: boolean
  workspace?: boolean
  navTitle?: string
  navIcon?: string
  navOrder?: number
}
```

Dashboard、Agents、MR、Issue、交付验收和流水线设为主导航；任务详情不出现在左侧，
但进入时以 `任务 #<id>` 创建动态 Tab。登录和初始化保持 `public: true`。

`AdminShell.vue` 使用 `RouterView` 插槽渲染当前页面。没有 Tab 时强制显示 Dashboard
作为无标签默认内容；点击导航时先 `openTab` 再 `router.push`。Tab 点击同步路由；
关闭激活 Tab 后跳转到 Store 选择的相邻路径，关闭到零个时跳转 `/dashboard`。

- [ ] 先写组件测试：折叠时只有图标和可访问名称，展开后显示文字。
- [ ] 写组件测试：点击导航创建唯一 Tab；全部关闭后 Dashboard 内容仍存在且 Tab 栏为空。
- [ ] 运行 `npm test -- --run src/components/AdminShell.test.ts`，确认因组件不存在而失败。
- [ ] 增加路由名称和元数据，实现 `AdminShell.vue` 的侧栏、Tab 栏和内容出口。
- [ ] 使用 `KeepAlive` 缓存已打开的路由组件，缓存键使用完整路径。
- [ ] 重跑组件与 Store 测试并确认通过。
- [ ] 提交 `功能：增加后台侧栏与标签壳层`。

#### 任务 3：根组件接入和页面去重

**文件**

- 修改：`web/src/App.vue`
- 修改：`web/src/App.test.ts`
- 修改：`web/src/views/Dashboard.vue`
- 修改：`web/src/views/Agents.vue`
- 修改：`web/src/views/TaskDetail.vue`
- 修改：`web/src/views/MergeRequests.vue`
- 修改：`web/src/views/Issues.vue`
- 修改：`web/src/views/Deliveries.vue`
- 修改：`web/src/views/Pipelines.vue`

`App.vue` 对公开路由直接渲染 `RouterView`，对后台路由渲染 `AdminShell`。各业务页面删除
重复的 `<header class="topbar">`、相关 `RouterLink` 和仅供旧导航使用的图标导入，
保留现有 `.console`、`.main` 和业务逻辑。

- [ ] 先扩展 `App.test.ts`，验证公开路由不渲染后台壳层、后台路由渲染壳层。
- [ ] 增加源码约束测试，验证七个业务页面不再包含 `<header class="topbar">`。
- [ ] 运行对应测试并确认因旧结构仍存在而失败。
- [ ] 接入 `AdminShell`，逐页删除旧顶部栏和无用导入。
- [ ] 运行 `npm test -- --run`，确认所有既有页面测试继续通过。
- [ ] 提交 `重构：统一后台页面导航入口`。

#### 任务 4：左右布局样式和窄屏行为

**文件**

- 修改：`web/src/style.css`
- 修改：`web/src/components/AdminShell.vue`
- 修改：`web/src/components/AdminShell.test.ts`

桌面端壳层使用 CSS Grid：折叠列 `72px`，展开列 `220px`，右侧列 `minmax(0, 1fr)`。
Tab 栏保持在内容区顶部并支持横向滚动。视口小于 `768px` 时侧栏作为覆盖内容的抽屉，
关闭抽屉不改变桌面端保存的折叠偏好。

- [ ] 先写测试：展开按钮更新 `aria-expanded`，移动菜单按钮控制抽屉可见状态。
- [ ] 运行组件测试并确认失败。
- [ ] 实现桌面双态侧栏、Tab 横向滚动、窄屏抽屉和键盘焦点样式。
- [ ] 重跑组件测试和全量前端测试。
- [ ] 提交 `样式：完成后台左右布局和移动端导航`。

#### 任务 5：构建、在线部署和归档

**文件**

- 修改：`wanxiangAgentWorkMission.md`
- 修改：`wanxiangAgent.md`（仅在生产部署和健康检查均通过后）

- [ ] 开发完成后先运行 `pm2 show wanxiang-agent` 和 `/api/health`，确认开发期间服务仍在线。
- [ ] 在 `web/` 运行 `npm test -- --run`。
- [ ] 在 `web/` 运行 `npm run build`，确认产物写入实际 `web/dist`。
- [ ] 检查生产 Nginx 站点根目录是否就是 `web/dist`；若不是，先记录真实目录和替换方式。
- [ ] 部署前记录现有静态资源状态，部署新 `web/dist`。
- [ ] 请求后台入口和静态资源，确认页面返回成功；登录后人工验证侧栏、Tab 和刷新恢复。
- [ ] 再次确认 `wanxiang-agent` 为 `online` 且 `/api/health` 返回 `{"ok":true}`。
- [ ] 把本 Mission 的实际测试、构建、部署和健康检查字段更新为最终结果。
- [ ] 全部通过后将 R001 标记为“已完成”，写入约 100 字 Mission 总结并清理本文件。
- [ ] 提交 `交付：完成后台导航与标签工作区`。
