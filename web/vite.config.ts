import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';

// Сборка кладётся в dist/app и вшивается в бинарник snag (web/embed.go).
// base './' — относительные пути: тот же билд работает и в корне сервера,
// и в демо по адресу вроде /snag-demo/.
export default defineConfig({
  plugins: [react()],
  base: './',
  build: {
    outDir: 'dist/app',
    emptyOutDir: true,
    target: 'es2022',
  },
  server: {
    proxy: { '/api': 'http://127.0.0.1:8000' },
  },
});
