# Agent 配置刷新与默认模型设计

## 目标

修复 Agent 密钥和运行配置已保存到私有 `env` 后，刷新页面仍显示陈旧 `blocked: missing_config` 的问题，并为新建或切换 Provider 的表单提供明确默认模型。

## 设计

- `agents/<name>/env` 继续作为 provider、API Key、Base URL 和 model 的持久真源，权限保持 `0600`，API 永不返回密钥。
- `loadRuntimeConfig` 完整验证 `env` 后，若数据库状态仅为陈旧的 `blocked: missing_config`，将其修正为 `configured`；其他状态如 `online`、`blocked: provider_error` 不改写。
- 保存已有 Agent 时空 `api_key` 必须保留 `env` 中原密钥。
- 前端默认模型为 OpenAI `gpt-5.2`、DeepSeek `deepseek-v4-flash`；仅用于新建、模型为空或切换 Provider，不覆盖已保存的非空模型。

## 验收

- 后端测试证明刷新读取会保留密钥并清理陈旧 missing-config 状态，响应只含 `secret_configured=true`。
- 前端测试证明新建和 Provider 切换应用正确默认模型。
- Go/Web 全量测试与构建通过。
