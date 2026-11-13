import { defineConfig } from 'vite';
export default defineConfig({
  server: {
    host: '127.0.0.1',
    port: 5174,
    strictPort: true,
    proxy: {
      '/api': { target: 'http://127.0.0.1:5080' },
      '/readyz': { target: 'http://127.0.0.1:5080' },
      '/healthz': { target: 'http://127.0.0.1:5080' },
      '/v2': { target: 'http://127.0.0.1:5080' },
      '/auth': { target: 'http://127.0.0.1:5080' },
    },
  },
  build: { outDir: 'dist', emptyOutDir: true },
});
