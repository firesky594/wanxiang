# Wanxiang Agent 当前 Mission

## R001 后台导航与 Tab 工作区返工

### Mission 状态

```yaml
requirement_id: R001
status: 正在开发
stage: 生产反馈已收到，等待完成鉴权与自适应导航链路诊断
frontend_build_required: true
frontend_build_command: npm test -- --run && npm run build
frontend_build_result: previous_passed_but_acceptance_failed
frontend_dist_path: web/dist
frontend_deployed: true
backend_build_required: pending_diagnosis
backend_build_result: not_run
backend_restart_required: pending_diagnosis
backend_restarted: false
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

### 返工范围

1. 追踪登录成功后 Token 的保存、请求头注入、后端 session 校验和 401 处理链路。
2. 追踪 `/api/events/stream` 的路由权限与 EventSource 鉴权方式，确认 SSE 断线根因。
3. 将左侧导航调整为基于现有 Element Plus 的自适应菜单组件；折叠时显示图标，展开后显示图标和文字。
4. 保留已确认的 Tab 打开、关闭、刷新恢复和零 Tab 显示 Dashboard 行为。
5. 增加导航展开、管理员 401 处理和 SSE 鉴权相关测试。
6. 重新构建并部署前端；如鉴权协议需要后端改动，则运行 Go 全量测试、构建、替换二进制并按 PM2 规则重启。

### 下一步

1. 读取后端管理员鉴权中间件、登录 handler、`admin_sessions` 校验和 SSE 路由注册。
2. 使用不回显 Token 的方式检查浏览器请求是否携带管理员凭据及数据库中是否存在有效 session。
3. 明确导航插件方案和 SSE 鉴权修复点后，先补失败测试再实施。
4. 开发和验证期间保持 `wanxiang-agent` 在线；需要替换后端二进制时才按流程重启。
5. 真实登录态下确认任务列表成功、SSE 在线、导航展开文字正常后，才能再次归档 R001。
