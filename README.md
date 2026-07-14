# Wanxiang Agent

Go API and Vue3 operations console for local multi-agent task orchestration.

## Local Development

Backend:

```bash
cd server
go test ./...
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
go test ./...
go build -o wanxiang ./cmd/wanxiang

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
