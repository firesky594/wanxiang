# Wanxiang Agent 当前 Mission

## R001 后台导航与 Tab 工作区返工

### Mission 状态

```yaml
requirement_id: R001
status: 正在开发
stage: 修复已部署，等待用户重新登录完成真实鉴权与 SSE 验收
frontend_build_required: true
frontend_build_command: npm test -- --run && npm run build
frontend_build_result: passed_32_tests_and_production_build
frontend_dist_path: web/dist
frontend_deployed: true
backend_build_required: true
backend_build_result: passed_go_test_all_and_go_build
backend_restart_required: true
backend_restarted: true
backend_process_manager: pm2
backend_pm2_app: wanxiang-agent
backend_pm2_status: online
backend_healthcheck_result: passed
```

### 用户反馈

2026-07-24，R001 生产验收未通过，用户反馈：

1. 左侧界面只有图标，点击后无法显示对应导航栏文字。
2. 当前导航交互不符合预期，应使用具备展开、折叠和响应式能力的自适应导航插件。
3. Dashboard 显示 SSE 断线。
4. 后台数据无响应，浏览器请求
   `GET http://t.agents.com/api/admin/tasks?limit=100&offset=0`
   返回 `401 Unauthorized`。
5. 自适应导航部署后，折叠状态下的菜单图标没有在侧栏中水平居中。
6. 第一次居中修复只让用户改名为“万象工作台”的顶部折叠按钮正确居中，下面的
   Element Plus 菜单图标仍未统一居中。

### 当前证据

- 当前 `AdminShell.vue` 使用自定义 `button + CSS Grid` 实现侧栏，没有使用 Element Plus
  的 `el-menu`、`el-aside` 等自适应导航组件。
- 当前导航项点击只执行打开或激活页面 Tab，不会展开侧栏文字；文字展开由侧栏底部独立按钮控制。
- 普通 API 客户端从 `localStorage.wanxiang_admin_token` 读取 Token，并通过
  `Authorization: Bearer <token>` 请求后台接口。
- SSE 当前使用原生 `EventSource('/api/events/stream')`，原生 EventSource 调用没有设置
  `Authorization` 请求头。
- PM2 中 `wanxiang-agent` 当前为 `online`，`/api/health` 返回成功。浏览器收到 401
  说明后端已响应，但管理员鉴权未通过；尚未确认是 Token 缺失、失效、会话记录不匹配，
  还是前后端 SSE 鉴权协议不一致。
- R001 之前的测试和构建虽然通过，但没有覆盖真实管理员登录态下 API 与 SSE 的生产验收，
  因此不能保持“已完成”状态。

### 根因与已部署修复

根因：

1. 生产数据库中的 3 个管理员 session 均已过期，最后一个 session 的过期时间为
   `2026-07-17T03:52:41.119638758Z`。
2. 前端路由守卫只判断本地 Token 字符串是否存在，过期 Token 仍可进入后台，随后任务 API
   和 SSE 一起收到 401。
3. 后端管理员鉴权优先 Bearer Token；Bearer 无效时不会继续尝试同源 HttpOnly Cookie。
4. 旧导航使用自定义按钮和 CSS，且旧版本地状态保存了折叠值，生产首次加载只显示图标。

已部署：

- 导航改为 Element Plus `el-container`、`el-aside`、`el-menu` 和 `el-menu-item`。
- 新版导航默认展开文字，使用顶部明确的菜单按钮折叠或重新展开，保留移动端抽屉。
- 工作区本地存储升级为 `wanxiang_workspace_v2`，不继承旧版只显示图标的折叠状态。
- 管理 API 收到 401 时清理过期 Token 和工作区状态，并跳转
  `/login?redirect=<原页面>`。
- 后端管理员鉴权在 Bearer 无效时继续校验有效 Cookie；鉴权完全失败时清理 HttpOnly Cookie。
- SSE 继续使用同源 EventSource 和登录 Cookie，不在 URL 中暴露 Token。
- 折叠侧栏增加专用 `is-collapsed` 状态，统一 Element Plus 折叠菜单与侧栏宽度为
  `72px`，并清除折叠菜单项的横向 padding 和图标 margin，使折叠按钮与菜单图标居中。
- 针对第一次修复未稳定命中插件内部节点的问题，折叠时为每个 `el-menu-item` 直接绑定
  `collapsed-menu-item`，固定菜单项可用宽度为 `52px` 并使用 CSS Grid 居中；每个图标
  统一绑定 `navigation-icon` 并强制清除 margin。保留用户设置的“万象工作台”名称。

部署验证：

```yaml
frontend_tests: 32 passed
frontend_build: passed
frontend_assets:
  js: /assets/index-Brt8TS_6.js
  css: /assets/index-BgWDc-H4.css
backend_tests: go test ./... passed
backend_build: passed
backend_pm2_status: online
backend_pm2_restarts: 1
backend_healthcheck: '{"ok":true}'
expired_session_browser_check: redirected_to_login_and_cleared_local_token
server_backup: /tmp/wanxiang-server-before-r001-rework-20260724T1024
frontend_backup: /tmp/wanxiang-web-dist-before-r001-rework-20260724T1024.tar.gz
```

### 返工范围

1. 追踪登录成功后 Token 的保存、请求头注入、后端 session 校验和 401 处理链路。
2. 追踪 `/api/events/stream` 的路由权限与 EventSource 鉴权方式，确认 SSE 断线根因。
3. 将左侧导航调整为基于现有 Element Plus 的自适应菜单组件；折叠时显示图标，展开后显示图标和文字。
4. 保留已确认的 Tab 打开、关闭、刷新恢复和零 Tab 显示 Dashboard 行为。
5. 增加导航展开、管理员 401 处理和 SSE 鉴权相关测试。
6. 重新构建并部署前端；如鉴权协议需要后端改动，则运行 Go 全量测试、构建、替换二进制并按 PM2 规则重启。

### 下一步

1. 用户刷新页面并使用管理员账号重新登录，生成新的 24 小时 session。
2. 登录后确认任务列表不再返回 401，Dashboard 显示 `SSE 在线`。
3. 确认左侧默认显示图标和文字，菜单按钮可以折叠并重新展开。
4. 以上真实登录验收通过后，才能将 R001 再次归档为“已完成”。

## 前端行为与测试目录规范化

2026-07-24 完成以下规范化工作：

1. 新增 `web/rules.md`，统一组件、导航、Tab、本地状态、API、鉴权、SSE、
   响应式样式、测试与部署行为。
2. 将前端现有 10 个测试文件从 `web/src/` 迁移至 `web/test/`，并按
   `api/`、`components/`、`stores/`、`views/` 镜像源码职责。
3. 调整测试导入路径及 Vite glob 路径，`web/src/` 已不存在
   `*.test.*` 或 `*.spec.*`。
4. `npm test` 固定只运行 `web/test/`。
5. 新任务为验证而新增的测试默认是临时文件：完成测试与生产构建、记录验证结果后必须删除；
   只有用户明确要求长期保留或迁移现有基线测试时才允许保留。

验证结果：

```yaml
frontend_test_directory: web/test
source_test_files: 0
baseline_test_files: 10
frontend_tests: 32 passed
frontend_build: passed
frontend_assets:
  js: /assets/index-Brt8TS_6.js
  css: /assets/index-BgWDc-H4.css
temporary_tests_added: 0
temporary_tests_remaining: 0
```

## 前端自定义函数中文注释与索引

### 执行范围

1. 仅处理 `web/src/` 内由项目自行定义的 TypeScript/Vue 函数。
2. 为自定义 API 方法、Store Action、组件方法和页面业务方法补充中文注释。
3. Vue、Pinia、Vue Router、Element Plus、Vue Flow、浏览器 API 等框架或插件自带方法不纳入。
4. `computed`、`onMounted`、`watch`、路由守卫等框架回调不作为独立自定义函数登记。
5. 在 `web/web_func_doc.md` 中按“序号. 路径 方法 50字内介绍”连续排序。

### 验证要求

```yaml
source_scope: web/src
comment_language: 中文
function_document: web/web_func_doc.md
frontend_tests_required: true
frontend_build_required: true
temporary_tests_allowed: false
runtime_must_remain_online: true
status: 已完成
```

### 执行结果

```yaml
documented_custom_functions: 89
source_chinese_function_comments: 112
framework_and_plugin_methods_documented: 0
function_document_numbering: continuous
descriptions_over_50_characters: 0
temporary_tests_added: 0
frontend_tests: 32 passed
frontend_build: passed
frontend_assets:
  js: /assets/index-mCUdmzLg.js
  css: /assets/index-B8k3V72h.css
backend_pm2_status: online
backend_healthcheck: '{"ok":true}'
```

## 后端规范、函数索引与测试归类

### 执行范围

1. 补全 `server/rules.md`，覆盖 Go 包职责、接口鉴权、数据库事务、并发任务、
   文件与命令安全、日志密钥、测试和 PM2 部署。
2. `server/server_func_doc.md` 只登记后端项目自定义且可跨文件复用的导出函数与方法，
   不记录 Go 标准库、Chi、SQLite 等框架或依赖方法。
3. 将现有后端测试源文件集中归类到 `server/test/`。
4. 同包白盒测试通过受控测试运行器以 Go overlay 虚拟映射回原包执行，
   不向源码目录写入测试文件，结束后自动清理临时 overlay 配置。
5. 保持 `wanxiang-agent` 在线，完成后执行全部后端测试、构建与健康检查。

### 当前状态

```yaml
source_scope: server
baseline_test_files: 66
baseline_test_lines: 6652
baseline_go_test: passed
test_directory: server/test
server_rules: server/rules.md
function_document: server/server_func_doc.md
temporary_tests_allowed: false
backend_pm2_status_before_change: online
status: 已完成
```

### 执行结果

```yaml
exported_functions_and_methods: 196
documented_functions_and_methods: 188
excluded_test_helpers_and_accessors: 8
chinese_go_doc_comments: 196
archived_baseline_test_files: 66
permanent_runner_guard_tests: 1
test_archive_path: server/test/testdata
test_runner: server/test/run.sh
test_transport: go_overlay
source_directory_test_files: 0
temporary_overlay_files_remaining: 0
test_archive_content_mismatches: 0
bare_go_test_guard: passed_by_expected_rejection
backend_tests: passed_66_archived_files_and_guard
backend_build: passed
backend_build_path: /tmp/wanxiang-agent-verify
backend_build_sha256: b122368a6835a3bcc899fed7e2e5f2a01fb0078202cdb44722387955ada19f56
backend_restart_required: false
backend_restart_reason: 仅注释、规范、函数索引与测试归档发生变化
backend_pm2_status: online
backend_healthcheck: '{"ok":true}'
```
