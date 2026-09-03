import tailwindcss from '@tailwindcss/vite';
import react from '@vitejs/plugin-react';
import { defineConfig, loadEnv } from 'vite';

// VITE_API_MOCK=false switches the mock worker off and proxies /api to the Go API at
// VITE_API_BASE_URL (default http://127.0.0.1:8080). Nothing else changes.
export default defineConfig(({ mode }) => {
  const env = loadEnv(mode, process.cwd(), 'VITE_');
  const target = env['VITE_API_BASE_URL'] || 'http://127.0.0.1:8080';
  return {
    plugins: [react(), tailwindcss()],
    server: {
      proxy: {
        '/api': { target, changeOrigin: false },
        '/health': { target, changeOrigin: false },
      },
    },
    build: {
      sourcemap: false,
      target: 'es2023',
    },
  };
});
