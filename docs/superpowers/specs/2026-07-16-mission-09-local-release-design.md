# M09 本机测试、重试、回滚和发布编排设计

## 目标与边界

M09 为项目提供声明式、本机、可审计的测试、构建、发布与回滚编排。首版不接入 SSH、Kubernetes 或云厂商。测试和构建可以自动执行；部署、生产迁移、删除和权限扩大必须由管理员针对具体运行单独确认，验收决定不等于发布授权。

## 元数据与状态

项目 `.wanxiang/project.yaml` 增加 `pipeline`：每个步骤声明 `id`、`kind`、`command`、`args`、`timeout_seconds`、`max_attempts`、`reversible`。服务端只接受 `go`、`npm`、`pnpm`、`node` 与 PM2 固定动作的参数数组，不经过 shell，不接受重定向、管道、变量展开或路径逃逸。

数据库追加 pipeline 定义快照、运行、步骤尝试和发布确认。运行状态为 `pending -> running -> passed|blocked|failed`；发布步骤为 `awaiting_confirmation -> running`。每个步骤以 `run_id + step_id` 幂等，成功步骤不会因 Worker 重启重复执行。

## 失败、重试与回滚

退出码和错误类型分为 `code_failure`、`environment_failure`、`provider_failure`、`permission_blocked`。只有环境或 Provider 临时失败能在 `max_attempts` 内重试；代码失败直接停止；权限阻塞等待确认。耗尽尝试后创建阻塞 Issue。

发布前记录项目 main commit、产物哈希和当前 PM2 二进制备份路径。发布失败只生成可确认的 rollback 步骤，不自动改写项目或生产；管理员确认回滚后恢复已记录安全版本并验证健康检查。不可逆步骤永不重试。

## API、界面与安全

管理员可查询流水线、启动测试/构建、确认发布或回滚；Agent Token 和匿名请求不得访问管理接口。页面展示步骤、尝试、分类、提交、产物哈希、确认人和回滚入口。日志、事件、数据库和响应统一脱敏。

## 验收

覆盖成功、四类失败、有限重试、重启恢复、重复请求、未确认发布拒绝、不可逆步骤不重试、发布失败生成回滚入口以及回滚到安全版本。
