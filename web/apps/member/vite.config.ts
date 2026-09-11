import tailwindcss from '@tailwindcss/vite';
import react from '@vitejs/plugin-react';
import { defineConfig, loadEnv } from 'vite';

// Same switch as the backoffice: VITE_API_MOCK=false proxies /api to VITE_API_BASE_URL.
// A build is served at /uye/ of the one origin the three apps share (deploy/nginx, deploy/caddy):
// one origin is one session cookie, which is what makes the single sign-in a single one.
// VITE_BASE_PATH overrides it; the development server always serves at /.
export default defineConfig(({ mode, command }) => {
  const env = loadEnv(mode, process.cwd(), 'VITE_');
  const target = env['VITE_API_BASE_URL'] || 'http://127.0.0.1:8080';
  return {
    base: command === 'build' ? env['VITE_BASE_PATH'] || '/uye/' : '/',
    plugins: [react(), tailwindcss()],
    server: {
      proxy: {
        '/api': { target, changeOrigin: false },
        '/health': { target, changeOrigin: false },
      },
    },
    build: { sourcemap: false, target: 'es2023' },
  };
});
