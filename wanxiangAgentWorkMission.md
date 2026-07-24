# Wanxiang Agent 当前 Mission

## R006 调度台 Agent 配置与任务抽屉修复

### Mission 状态

```yaml
requirement_id: R006
status: 正在开发
branch: feature/r006-agent-config-drawers
remote_branch_allowed: false
source_scope:
  - web/src/router.ts
  - web/src/views/Dashboard.vue
  - web/src/components/AgentCanvas.vue
  - web/web_func_doc.md
frontend_tests_required: true
frontend_build_required: true
temporary_tests_must_be_removed: true
backend_change_required: false
runtime_before_change:
  wanxiang-agent: online
  wanxiang-web-dev: online
  healthcheck: passed
```

### 当前证据

1. Agents 左侧导航由 `/agents` 路由的 `navOrder` 元数据生成；隐藏导航无需删除路由。
2. Agent 画布卡片当前没有点击事件，无法打开对应配置。
3. “新建任务”和“任务列表”按钮已正确修改抽屉状态，但浏览器实测
   `.el-drawer` 宽度为 `0px`。
4. 当前 Element Plus Drawer 使用 Splitter 计算宽度，
   `size="min(430px, 94vw)"` 被解析为 `flex-basis: 0px`，这是按钮看似无响应的根因。
5. `/api/admin/agents`、保存配置和重新探测接口已经存在，不需要修改后端。

### 验收要求

1. 左侧导航不再显示 Agents，但 `/agents`、旧标签恢复和初始化重定向保持兼容。
2. Agent 卡片可通过鼠标和键盘打开对应配置面板。
3. 配置面板展示名称、状态、Provider、模型、Base URL、密钥配置和最近错误，
   并保留已有配置保存与重新探测能力；不回显已有密钥。
4. 新建任务和任务列表按钮打开宽度正确、可关闭的自适应抽屉。
5. 临时测试通过后删除，再执行全部基线测试和生产构建。
6. 最终确认 PM2 服务持续在线、健康接口通过，开发分支不推送远端。
