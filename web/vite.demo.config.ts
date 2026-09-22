import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';

// Сборка демо для статического хостинга: интерфейс (app.html) и страница
// демо (index.html) рядом со snag.wasm.gz. Собирается скриптом
// scripts/build-demo.mjs, он же переименовывает страницы и кладёт wasm.
export default defineConfig({
  plugins: [react()],
  base: './',
  build: {
    outDir: 'dist/demo',
    emptyOutDir: true,
    target: 'es2022',
    rollupOptions: {
      input: { app: 'index.html', demo: 'demo.html' },
    },
  },
});
