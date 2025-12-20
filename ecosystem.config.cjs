module.exports = {
  apps: [
    {
      name: 'cli-proxy-api',
      cwd: '/Users/user/claude-relay-service/CLIProxyAPI',
      script: './cli-proxy-api',
      interpreter: 'none',
      watch: false,
      autorestart: true,
      max_restarts: 10,
      env: {
        NODE_ENV: 'production'
      }
    },
    {
      name: 'cli-proxy-management',
      cwd: '/Users/user/claude-relay-service/CLIProxyAPI/Cli-Proxy-API-Management-Center',
      script: 'npx',
      args: 'serve . -l 3090',
      interpreter: 'none',
      watch: false,
      autorestart: true,
      max_restarts: 10,
      env: {
        NODE_ENV: 'production'
      }
    }
  ]
};
