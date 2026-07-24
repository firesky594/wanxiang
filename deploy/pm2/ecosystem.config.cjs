module.exports = {
  apps: [
    {
      name: "wanxiang-agent",
      namespace: "wanxiang",
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
    {
      name: "wanxiang-web-dev",
      namespace: "wanxiang",
      script: "npm",                     
      args: "run dev",                  
      cwd: "/www/wwwroot/t.agents.com/wanxiang/web",
      interpreter: "none",              
      exec_mode: "fork",
      autorestart: true,
      watch: false,                     
      env: {
        NODE_ENV: "development",
        PORT: "5173" 
      }
    }
  ],
};
