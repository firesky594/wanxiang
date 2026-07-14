# Wanxiang Agent PM2 Migration Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Register the existing Wanxiang Agent binary with the root PM2 daemon and preserve it across server reboots.

**Architecture:** A CommonJS ecosystem file defines one fork-mode PM2 process with an absolute binary path, an explicit working directory, and the two required environment variables. PM2 owns restart and log handling, while systemd restores the root PM2 process list after a reboot.

**Tech Stack:** PM2 7.0.1, systemd, Go executable, Node.js 26 for configuration validation

## Global Constraints

- Run `/www/wwwroot/t.agents.com/wanxiang-agent/server/wanxiang` without rebuilding it.
- Bind the API to `127.0.0.1:8088`.
- Use `/www/wwwroot/t.agents.com/wanxiang-agent` for both `cwd` and `WANXIANG_ROOT`.
- Keep the existing `zt-docs` PM2 process running and saved.
- Leave `README.md`, `agents/`, Nginx, and BaoTa Supervisor unchanged.

---

### Task 1: Add the PM2 Ecosystem File

**Files:**

- Create: `deploy/pm2/ecosystem.config.cjs`
- Test: inline Node.js assertions against `deploy/pm2/ecosystem.config.cjs`

**Interfaces:**

- Consumes: the existing `server/wanxiang` executable and project root directories
- Produces: one PM2 app definition named `wanxiang-agent`

- [ ] **Step 1: Run the configuration assertion before creating the file**

Run:

```bash
node -e "require('./deploy/pm2/ecosystem.config.cjs')"
```

Expected: FAIL with `MODULE_NOT_FOUND` because the ecosystem file does not exist.

- [ ] **Step 2: Create the ecosystem file**

Create `deploy/pm2/ecosystem.config.cjs`:

```javascript
module.exports = {
  apps: [
    {
      name: "wanxiang-agent",
      script: "/www/wwwroot/t.agents.com/wanxiang-agent/server/wanxiang",
      cwd: "/www/wwwroot/t.agents.com/wanxiang-agent",
      interpreter: "none",
      exec_mode: "fork",
      instances: 1,
      autorestart: true,
      watch: false,
      restart_delay: 3000,
      env: {
        WANXIANG_ROOT: "/www/wwwroot/t.agents.com/wanxiang-agent",
        WANXIANG_HTTP_ADDR: "127.0.0.1:8088",
      },
    },
  ],
};
```

- [ ] **Step 3: Validate every required PM2 field**

Run:

```bash
node -e "const assert=require('node:assert/strict'); const config=require('./deploy/pm2/ecosystem.config.cjs'); assert.equal(config.apps.length,1); const app=config.apps[0]; assert.equal(app.name,'wanxiang-agent'); assert.equal(app.script,'/www/wwwroot/t.agents.com/wanxiang-agent/server/wanxiang'); assert.equal(app.cwd,'/www/wwwroot/t.agents.com/wanxiang-agent'); assert.equal(app.interpreter,'none'); assert.equal(app.exec_mode,'fork'); assert.equal(app.instances,1); assert.equal(app.autorestart,true); assert.equal(app.watch,false); assert.equal(app.restart_delay,3000); assert.deepEqual(app.env,{WANXIANG_ROOT:'/www/wwwroot/t.agents.com/wanxiang-agent',WANXIANG_HTTP_ADDR:'127.0.0.1:8088'}); console.log('ecosystem config valid')"
```

Expected: PASS with `ecosystem config valid`.

- [ ] **Step 4: Commit the deployment configuration**

```bash
git add deploy/pm2/ecosystem.config.cjs docs/superpowers/plans/2026-07-13-wanxiang-agent-pm2.md
git commit -m "ops: manage wanxiang agent with PM2"
```

### Task 2: Start and Verify the Process

**Files:** None. This task changes root PM2 runtime state.

**Interfaces:**

- Consumes: `deploy/pm2/ecosystem.config.cjs`
- Produces: one online PM2 process and a healthy loopback API

- [ ] **Step 1: Confirm that no old instance owns the name or port**

Run:

```bash
pm2 jlist
ss -ltnp 'sport = :8088'
```

Expected: PM2 has no `wanxiang-agent` entry, and `ss` has no listener on port `8088`.

- [ ] **Step 2: Start the ecosystem process**

Run:

```bash
pm2 start deploy/pm2/ecosystem.config.cjs --only wanxiang-agent
```

Expected: PM2 reports `wanxiang-agent` as `online` without changing `zt-docs`.

- [ ] **Step 3: Check the application health endpoint**

Run:

```bash
curl --noproxy '*' --fail --silent --show-error http://127.0.0.1:8088/api/health
```

Expected: `{"ok":true}`.

- [ ] **Step 4: Verify the effective runtime configuration**

Run:

```bash
pm2 describe wanxiang-agent
pm2 jlist | node -e "const assert=require('node:assert/strict'); let input=''; process.stdin.on('data',chunk=>input+=chunk); process.stdin.on('end',()=>{const app=JSON.parse(input).find(item=>item.name==='wanxiang-agent'); assert.ok(app); assert.equal(app.pm2_env.status,'online'); assert.equal(app.pm2_env.pm_cwd,'/www/wwwroot/t.agents.com/wanxiang-agent'); assert.equal(app.pm2_env.WANXIANG_ROOT,'/www/wwwroot/t.agents.com/wanxiang-agent'); assert.equal(app.pm2_env.WANXIANG_HTTP_ADDR,'127.0.0.1:8088'); console.log('runtime config valid')})"
```

Expected: `pm2 describe` reports fork mode, and the assertion prints `runtime config valid`.

- [ ] **Step 5: Roll back only the new entry if health validation fails**

Run these commands only after a failed health check:

```bash
pm2 logs wanxiang-agent --lines 100 --nostream
pm2 delete wanxiang-agent
```

Expected: PM2 removes `wanxiang-agent` and keeps `zt-docs` online.

### Task 3: Save and Verify Reboot Persistence

**Files:** Root PM2 dump and the systemd unit generated by PM2.

**Interfaces:**

- Consumes: the healthy root PM2 process list
- Produces: an enabled `pm2-root.service` and a saved process dump containing both services

- [ ] **Step 1: Inspect the current root PM2 startup unit**

Run:

```bash
systemctl is-enabled pm2-root.service
systemctl status pm2-root.service --no-pager
```

Expected: the commands show whether PM2 has installed and enabled the unit. A missing unit is acceptable before the next step.

- [ ] **Step 2: Install the root PM2 startup unit if it is missing**

Run this command only when `pm2-root.service` does not exist:

```bash
env PATH=/www/server/nodejs/v26.0.0/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin pm2 startup systemd -u root --hp /root
```

Expected: PM2 creates and enables `pm2-root.service`.

- [ ] **Step 3: Save the healthy PM2 process list**

Run:

```bash
pm2 save
```

Expected: PM2 writes `/root/.pm2/dump.pm2`.

- [ ] **Step 4: Validate the saved process names and startup unit**

Run:

```bash
node -e "const fs=require('node:fs'); const apps=JSON.parse(fs.readFileSync('/root/.pm2/dump.pm2','utf8')); const names=apps.map(app=>app.name).sort(); for (const required of ['wanxiang-agent','zt-docs']) if (!names.includes(required)) throw new Error('missing '+required); console.log(names.join('\n'))"
systemctl is-enabled pm2-root.service
pm2 list
```

Expected: output contains `wanxiang-agent` and `zt-docs`, systemd reports `enabled`, and PM2 reports both processes as `online`.
