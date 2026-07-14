# Mission 03 Agent 匹配实施计划

> **执行要求：** 使用 superpowers:executing-plans 按任务执行，所有行为先写失败测试。

**目标：** 按 Provider、能力、Skill、MCP、项目权限、并发上限和负载过滤 Agent，保存可解释评分，并支持用户覆盖匹配结果。

**架构：** Agent 定义加载器只读取非密钥元数据；匹配器执行硬条件过滤和稳定排序；调度服务把选择、拒绝理由和团队负责人决策写入 SQLite；管理 API 和 Web 页面展示并允许用户覆盖。

**技术栈：** Go 1.26、SQLite、Vue 3、Pinia、Vitest。

## 全局约束

- 匹配器不得读取或返回 API 密钥。
- 硬条件失败的 Agent 不参与评分。
- 相同输入产生相同排序；同分时按 Agent 名称排序。
- 覆盖匹配需要管理员身份并写入审计事件。
- Git 提交说明使用中文。

### Task 1：Agent 非密钥定义加载器

**文件：** `server/internal/matching/definition.go`、`definition_test.go`

- [x] 先测试 role、capabilities、max_concurrency、project_access、skills、mcps 和 memory 摘要加载。
- [x] 测试路径越界、符号链接、无效名称和包含密钥字段时拒绝加载。
- [x] 实现受限 YAML 字段解析和目录清单读取，不增加密钥依赖。
- [x] 运行 `go test ./internal/matching -run TestLoadDefinition`。

### Task 2：硬条件过滤和解释评分

**文件：** `server/internal/matching/matcher.go`、`matcher_test.go`

- [x] 测试离线、能力、Skill、MCP、项目权限和并发上限过滤。
- [x] 测试记忆命中、空闲度和能力冗余评分及稳定排序。
- [x] 实现 `Match(workItem, agents) MatchResult`，返回候选和拒绝理由。
- [x] 运行 matching 包测试。

### Task 3：匹配决策持久化和团队负责人判断

**文件：** `server/internal/db/migrations.go`、`server/internal/assignments/`

- [x] 测试选择结果、评分、拒绝理由、负责人决策和幂等写入。
- [x] 新增匹配决策与 assignment 表。
- [x] 多工作包共享依赖、多人修改或高风险时设置项目负责人。
- [x] 没有候选时创建非密钥 Agent 定义并进入 `blocked: missing_config`。

### Task 4：自动匹配、恢复和管理员覆盖 API

**文件：** `server/internal/app/`、`server/internal/httpapi/`、`web/src/`

- [ ] planned 任务自动进入匹配；Probe 成功后恢复 missing_config 任务。
- [ ] 增加匹配结果查询和管理员覆盖 API。
- [ ] Web 任务详情展示候选、评分、拒绝原因和负责人。
- [ ] 覆盖操作写入 runtime_events 和 audit_logs。

### Task 5：交付验证

- [ ] 运行完整 Go 测试和后端构建。
- [ ] 运行前端测试并生成 `web/dist`。
- [ ] 记录前端构建、部署和后端重启判断。
- [ ] 更新 Mission，合并到 main 并推送 origin/main。
