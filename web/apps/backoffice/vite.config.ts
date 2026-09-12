import tailwindcss from '@tailwindcss/vite';
import react from '@vitejs/plugin-react';
import { defineConfig, loadEnv } from 'vite';

// VITE_API_MOCK=false switches the mock worker off and proxies /api to the Go API at
// VITE_API_BASE_URL (default http://127.0.0.1:8080).
//
// This server is also the single door in development (scripts/dev.ps1 up): the provider portal
// and the member app run on their own ports under /portal/ and /uye/, and those paths are handed
// through from here, hot-reload sockets included. One origin is one session cookie, which is what
// makes the single sign-in single — and it is what a deployment does with one domain.
export default defineConfig(({ mode }) => {
  const env = loadEnv(mode, process.cwd(), 'VITE_');
  const target = env['VITE_API_BASE_URL'] || 'http://127.0.0.1:8080';
  const portal = env['VITE_PROVIDER_DEV_URL'] || 'http://127.0.0.1:5182';
  const member = env['VITE_MEMBER_DEV_URL'] || 'http://127.0.0.1:5183';
  return {
    plugins: [react(), tailwindcss()],
    server: {
      proxy: {
        '/api': { target, changeOrigin: false },
        '/health': { target, changeOrigin: false },
        '/portal': { target: portal, changeOrigin: false, ws: true },
        '/uye': { target: member, changeOrigin: false, ws: true },
      },
    },
    build: {
      sourcemap: false,
      target: 'es2023',
    },
  };
});
