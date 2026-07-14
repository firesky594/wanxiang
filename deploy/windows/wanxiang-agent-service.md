# Windows Service

Build `server/cmd/wanxiang` as `wanxiang.exe`.

Set environment variables:

- `WANXIANG_ROOT=C:\wanxiang-agent`
- `WANXIANG_HTTP_ADDR=:8088`

Run the executable behind IIS, Nginx, or Caddy reverse proxy. Use a service wrapper such as NSSM or Windows Service Control integration added in a later hardening task.
