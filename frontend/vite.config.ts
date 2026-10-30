/// <reference types="vitest/config" />
import { fileURLToPath, URL } from 'node:url';

import { defineConfig } from 'vitest/config';
import vue from '@vitejs/plugin-vue';
import tailwindcss from '@tailwindcss/vite';
import wails from '@wailsio/runtime/plugins/vite';

// Wails serves frontend/dist from Go. `wails3 dev` runs this server and points
// the webview at it, so the port must stay fixed (strictPort).
//
// The wails() plugin rewrites the generated bindings' `/wails/runtime.js`
// import to the npm package, which is what lets Vite bundle them at all. It is
// left out under vitest: unit tests exercise our code, not the dev server.
const plugins = [vue(), tailwindcss(), ...(process.env.VITEST ? [] : [wails('./bindings')])];

export default defineConfig({
  plugins,
  resolve: {
    alias: [
      { find: '@', replacement: fileURLToPath(new URL('./src', import.meta.url)) },
      // The generated bindings import the runtime from the URL the Go asset
      // server serves. In a build the wails() plugin rewrites that; under
      // vitest there is no asset server, so point it at the npm package.
      ...(process.env.VITEST ? [{ find: /^\/wails\/runtime\.js$/, replacement: '@wailsio/runtime' }] : []),
    ],
  },
  server: {
    port: 5173,
    strictPort: true,
  },
  build: {
    // WebView2 ships a current Chromium; no need to down-level.
    target: 'chrome120',
    outDir: 'dist',
    emptyOutDir: true,
    sourcemap: false,
    // xterm + its renderers are one chunk whether we like it or not.
    chunkSizeWarningLimit: 1200,
  },
  clearScreen: false,
  test: {
    environment: 'happy-dom',
    setupFiles: ['src/test-setup.ts'],
    include: ['src/**/*.test.ts'],
  },
});
