# Agent Provider v0.1.0 Design

## Goal

Give each local agent its own model provider, API key, base URL, and model. The
Go service selects an OpenAI or DeepSeek adapter from the agent's configuration
and verifies the configuration with a real API request before reporting the
agent online.

## Configuration

Each `agents/<name>/env` file stores:

```text
AGENT_PROVIDER_TYPE=openai
AGENT_API_KEY=
AGENT_BASE_URL=https://api.openai.com/v1
AGENT_MODEL=
```

DeepSeek uses `https://api.deepseek.com` by default. A non-empty
`AGENT_BASE_URL` overrides the provider default. `AGENT_MODEL` stays required
because model availability changes independently of this application.

The service writes `env` with mode `0600`. Git ignores it. Admin responses only
return `secret_configured`; no endpoint returns the API key.

## Provider boundary

The `providers` package exposes a registry keyed by `openai` and `deepseek`.
Each adapter owns its URL construction, authorization, request payload, response
parsing, and provider error parsing. Both v0.1.0 adapters call
`POST /chat/completions`, but they remain separate implementations.

The startup probe sends one minimal user message with a one-token response limit.
The launcher probes once after configuration changes or process startup. It does
not poll the paid API. A successful response marks the agent online. Missing
configuration and provider failures produce explicit blocked states.

## Admin API and UI

Authenticated admins can list agents, read non-secret model configuration, save
configuration, and request a probe. The Agents page provides an agent name,
provider selector, model, base URL, optional replacement API key, save action,
and probe result. Leaving the API key blank preserves an existing secret.

## Compatibility

The manager retains its existing directory. A legacy `MANAGER_API_KEY` value is
accepted as an API-key fallback, while all newly saved configuration uses the
`AGENT_*` names.

## Versioning and verification

Implementation uses small commits on `main`, as authorized. Backend provider
tests use local HTTP test servers. The release requires all Go and frontend
tests, both production builds, a local runtime health check, and tag `v0.1.0`.
