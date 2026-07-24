# Wanxiang Agent 当前 Mission

## R006 调度台 Agent 配置与任务抽屉修复

### Mission 状态

```yaml
requirement_id: R006
status: 验收通过
branch: feature/r006-agent-config-drawers
remote_branch_allowed: false
source_scope:
  - web/src/router.ts
  - web/src/views/Dashboard.vue
  - web/src/components/AgentCanvas.vue
  - web/src/components/AgentConfigPanel.vue
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

### 验收结果

1. `/agents` 路由仅移除 `navOrder`，左侧导航不再显示 Agents；路由本身、
   旧标签打开方式和 `/init-manager` 重定向继续可用。
2. Agent 画布卡片支持鼠标、Enter 和 Space 打开对应配置抽屉，节点及卡片均补充
   名称、连接状态和键盘焦点反馈。
3. 新增 `AgentConfigPanel.vue` 展示脱敏配置，并复用现有保存与重新探测接口；
   已保存密钥不回显，两个真实探测动作互斥，避免并发状态竞态。
4. Element Plus Drawer 改用数值像素宽度：桌面为 430px，375px 视口下为
   352px；新建任务、任务列表和 Agent 配置三个入口均可见且可关闭。
5. 任务列表打开时主动刷新，并在抽屉内展示加载和请求错误，避免空白抽屉再次
   表现为“点击没反应”。
6. 第一轮临时测试 3 个文件、7 个用例全部通过；审查修复临时测试 1 个文件、
   2 个用例通过，全部测试文件随后已从 `web/test/` 删除。
7. 删除临时测试后执行 `npm test -- --run`：10 个文件、32 个用例全部通过。
8. 最终执行 `npm run build` 通过，产物为 `index-pMHlM9uF.css`、
   `index-CimfwjPf.js`；仅保留既有大分包提示。
9. 浏览器验收：
   - 1440×900 下左侧导航无 Agents，任务列表和 Agent 配置抽屉宽度均为
     430px，Splitter `flex-basis` 为 430px。
   - manager 卡片通过键盘 Enter 打开配置；API Key 输入值为空，未发现密钥类
     localStorage 项，任务列表正确显示测试数据。
   - 375×812 下新建任务按钮具有可访问名称，抽屉宽度为 352px。
   - `/agents` 可直接访问，`/init-manager` 正确重定向至 `/agents`，
     全程浏览器控制台无错误。
10. 最终运行状态：`wanxiang-agent` PID 934924、`wanxiang-web-dev`
    PID 990807，均与开发前一致；`curl --noproxy '*' -fsS
    http://127.0.0.1:8088/api/health` 返回 `{"ok":true}`，未修改或重启后端。
11. 功能分支只存在于本地，未推送到远端。
