import tailwindcss from '@tailwindcss/vite';
import react from '@vitejs/plugin-react';
import { defineConfig, loadEnv } from 'vite';

// Same switch as the backoffice: VITE_API_MOCK=false proxies /api to VITE_API_BASE_URL.
// A build is served at /portal/ of the one origin the three apps share (deploy/nginx, deploy/caddy):
// one origin is one session cookie, which is what makes the single sign-in a single one.
// VITE_BASE_PATH overrides it, and in development it is what puts this app behind the single
// door of scripts/dev.ps1 up: served at /portal/ of the door's port, with its hot-reload socket going
// back through that port (VITE_DEV_DOOR_PORT) rather than its own.
export default defineConfig(({ mode, command }) => {
  const env = loadEnv(mode, process.cwd(), 'VITE_');
  const target = env['VITE_API_BASE_URL'] || 'http://127.0.0.1:8080';
  const door = Number(env['VITE_DEV_DOOR_PORT'] ?? 0);
  return {
    base: env['VITE_BASE_PATH'] || (command === 'build' ? '/portal/' : '/'),
    plugins: [react(), tailwindcss()],
    server: {
      proxy: {
        '/api': { target, changeOrigin: false },
        '/health': { target, changeOrigin: false },
      },
      ...(door > 0 ? { hmr: { clientPort: door } } : {}),
    },
    build: { sourcemap: false, target: 'es2023' },
  };
});
