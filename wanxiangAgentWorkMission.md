# Wanxiang Agent 当前 Mission

## R007 总管持续巡检、任务单次提交与项目自动执行

### Mission 状态

```yaml
requirement_id: R007
status: 正在开发
branch: feature/r007-manager-supervision-execution
remote_branch_allowed: false
frontend_build_required: true
backend_build_required: true
backend_restart_required: true
temporary_tests_must_be_removed: true
runtime_before_change:
  wanxiang-agent:
    pid: 934924
    status: online
  wanxiang-web-dev:
    pid: 990807
    status: online
  backend_healthcheck: passed
  frontend_http: 200
source_scope:
  - agents/manager/system_prompt.md
  - agents/manager/memory/
  - server/internal/agents/
  - server/internal/app/
  - server/internal/tasks/
  - server/internal/executor/
  - server/internal/httpapi/
  - server/server_func_doc.md
  - web/src/views/Dashboard.vue
  - web/web_func_doc.md
```

### 用户需求

1. 总管 Agent 必须持续检测项目状态并保持活跃，通过
   `agents/manager/system_prompt.md` 与持久 Memory 管理现有 Agent。
2. 新增任务成功后抽屉应自动关闭，提交期间和成功后不得二次点击造成重复创建。
3. Project 建立后必须进入 Agent 规划、分配和执行链路；无法执行时显示明确阻塞原因。

### 当前证据

1. 开始任务前 `agents/manager/system_prompt.md` 已有用户未提交修改，新增了最低角色
   集合和 Agent 命名规则；本任务必须保留并在此基础上完善。
2. `agents/manager/` 当前没有持久 `memory/` 内容，总管规则无法跨运行周期固化。
3. 调度台创建任务成功后保留抽屉和表单，用户可以再次提交相同任务。
4. 项目创建、规划、匹配、租约和执行分别存在独立领域，需要核对应用装配是否把
   新任务持续推进到可执行 Agent。

### 验收要求

1. 总管具备受应用生命周期管理的周期巡检，不使用无间隔循环；服务重启后继续工作。
2. 总管活跃状态、项目巡检结果和 Agent 管理决策有持久记录且不包含密钥。
3. 总管提示词和 Memory 明确最低角色、命名、状态检查、复用、补充及安全清理边界。
4. 新建任务请求期间禁止重复提交，成功后重置表单并关闭抽屉；失败时保留输入并显示错误。
5. Project 建立后自动进入规划、匹配、工作区和执行链；缺少合适 Agent 时进入可见阻塞状态。
6. 临时前后端测试通过后删除，重新执行全部基线测试、构建、浏览器和运行状态验收。
7. 完成后清空 Mission，合并到本地 `main`，仅推送 `main` 并确认与 `origin/main` 同步。
