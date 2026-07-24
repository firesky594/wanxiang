# Wanxiang Agent 当前 Mission

## R005 任务调度台全画布 Agent 视图

### Mission 状态

```yaml
requirement_id: R005
status: 正在开发
branch: feature/r005-agent-canvas
remote_branch_allowed: false
source_scope:
  - web/src/views/Dashboard.vue
  - web/src/components/AgentCanvas.vue
  - web/src/assets/
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

1. `Dashboard.vue` 当前使用指标、事件图、新建任务和持久任务卡片组成两列页面，
   页面主体不是统一画布。
2. `WorkflowGraph.vue` 只把运行事件渲染为流程节点，不展示现有 Agent 配置。
3. `/api/admin/agents` 已返回 Agent 名称、模型和原始状态，当前登记 3 个 Agent：
   `manager` 为 `online`，`auto-backend-2` 与 `auto-docs-1` 为
   `blocked: missing_config`。
4. 项目已安装 Vue Flow，可直接提供缩放、平移和节点拖动，不新增前端依赖。

### 验收要求

1. 任务调度台的可用内容区域全部使用画布，不再保留固定卡片网格。
2. 每个现有 Agent 以本地图片节点出现，节点顶部显示 Agent 名称。
3. `online` 或 `busy` 使用绿点，其余状态使用红点，并显示可读状态文本。
4. 用户可以拖动 Agent，位置使用带版本号的本地存储保存，刷新和状态更新后不重置。
5. 保留新建任务、持久任务入口与 SSE 状态，不丢失现有调度能力。
6. 临时测试通过后删除，随后重新执行现有测试和生产构建。
