module.exports = {
  apps: [
    {
      name: "wanxiang-agent",
      script: "/www/wwwroot/t.agents.com/wanxiang/server/wanxiang",
      cwd: "/www/wwwroot/t.agents.com/wanxiang",
      interpreter: "none",
      exec_mode: "fork",
      instances: 1,
      autorestart: true,
      watch: false,
      restart_delay: 3000,
      env: {
        WANXIANG_ROOT: "/www/wwwroot/t.agents.com/wanxiang",
        WANXIANG_HTTP_ADDR: "127.0.0.1:8088",
      },
    },
  ],
};
