# Wanxiang Agent

Go API and Vue3 operations console for local multi-agent task orchestration.

## Local Development

Backend:

```bash
cd server
./test/run.sh ./...
go run ./cmd/wanxiang
```

Frontend:

```bash
cd web
npm install
npm run dev
```

## Production Deployment

Build the backend and frontend from the project root:

```bash
cd server
./test/run.sh ./...
go build -o /tmp/wanxiang-agent-verify ./cmd/wanxiang

cd ../web
npm ci
npm test
npm run build
```

Run the API with PM2:

```bash
cd /www/wwwroot/t.agents.com/wanxiang
pm2 start deploy/pm2/ecosystem.config.cjs --only wanxiang-agent
pm2 save
```

The production API listens on `127.0.0.1:8088`. Runtime state is stored under
`data/`, agent definitions under `agents/`, and managed workspaces under
`projects/`. Back up these directories before replacing or removing a deployment.

Verify the API without using a shell HTTP proxy:

```bash
curl --noproxy '*' http://127.0.0.1:8088/api/health
```

Rollback by stopping the PM2 process, restoring the previous deployment path in
the PM2 ecosystem file, starting it again, and running `pm2 save`.

## Agent model providers

Each agent keeps its private model settings in `agents/<name>/env`:

```dotenv
AGENT_PROVIDER_TYPE=openai
AGENT_API_KEY=replace-with-a-private-key
AGENT_BASE_URL=https://api.openai.com/v1
AGENT_MODEL=replace-with-an-available-model
```

Set `AGENT_PROVIDER_TYPE=deepseek` to use the DeepSeek adapter. Its default base
URL is `https://api.deepseek.com`. The admin Agents page applies these defaults,
allows a per-agent base URL override, and performs one minimal chat request when
you save or probe a configuration. The service does not poll paid APIs.

The backend writes each `env` file with permission `0600`. Git ignores these
files, and admin responses return only whether a secret exists. Leaving the API
key blank while editing preserves the current key.
