# Wanxiang Agent PM2 Process Management Design

## Goal

Run the existing `server/wanxiang` binary under the root PM2 daemon, keep the API bound to `127.0.0.1:8088`, and restore the process after a server reboot.

## Current State

- The root PM2 daemon runs from `/root/.pm2` and already manages `zt-docs`.
- No process listens on port `8088`.
- BaoTa Supervisor has no `wanxiang-agent` program to stop.
- The repository contains local changes in `README.md` and `agents/`; this migration must leave them untouched.
- The existing binary embeds revision `63a60cb`, while the repository is at `9782e7e`. This migration runs that binary as requested and does not rebuild it.

## PM2 Configuration

Create `deploy/pm2/ecosystem.config.cjs` with one fork-mode process:

- name: `wanxiang-agent`
- script: `/www/wwwroot/t.agents.com/wanxiang-agent/server/wanxiang`
- cwd: `/www/wwwroot/t.agents.com/wanxiang-agent`
- interpreter: `none`
- instances: `1`
- automatic restart: enabled with a three-second delay
- file watching: disabled
- `WANXIANG_ROOT=/www/wwwroot/t.agents.com/wanxiang-agent`
- `WANXIANG_HTTP_ADDR=127.0.0.1:8088`

PM2 writes stdout and stderr to the root PM2 log directory. The application continues to store its database, agent files, and project worktrees under `WANXIANG_ROOT`.

## Activation

Start the ecosystem file with the existing root PM2 daemon. Save the PM2 process list after the process becomes healthy. Keep the existing `zt-docs` entry in the saved list.

Use PM2's root systemd startup unit for reboot persistence. If the unit already exists, verify that systemd enables it. If it does not exist, generate it for user `root` with home directory `/root`, enable it, then save the PM2 process list again.

## Validation

The migration succeeds when all checks pass:

1. PM2 reports one online `wanxiang-agent` instance in fork mode.
2. The process environment contains the required root directory and loopback HTTP address.
3. `http://127.0.0.1:8088/api/health` returns HTTP 200 with `{"ok":true}`.
4. The saved PM2 dump contains both `wanxiang-agent` and the pre-existing `zt-docs` process.
5. The root PM2 systemd unit is enabled.

## Failure and Rollback

If the process does not become healthy, inspect the PM2 error log and leave `zt-docs` running. Remove only the `wanxiang-agent` PM2 entry, save the corrected process list, and keep BaoTa Supervisor itself running. Restoring BaoTa management requires creating a new program because its current configuration is empty.
