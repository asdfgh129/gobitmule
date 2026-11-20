import { fileURLToPath, URL } from 'node:url'
import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'
import { viteSingleFile } from 'vite-plugin-singlefile'

// The daemon embeds a single HTML file (internal/api/ui/index.html, go:embed).
// Inlining keeps the Go side unchanged: no asset routes, no SPA fallback, no
// cache-busting contract. Hash routing keeps deep links on the same document.
export default defineConfig({
  base: './',
  plugins: [vue(), viteSingleFile()],
  resolve: {
    alias: { '@': fileURLToPath(new URL('./src', import.meta.url)) },
  },
  build: {
    outDir: '../internal/api/ui',
    emptyOutDir: true,
    cssCodeSplit: false,
    assetsInlineLimit: 100_000_000,
    rollupOptions: { output: { inlineDynamicImports: true } },
  },
  server: {
    port: 5173,
    // The daemon has no CORS headers; proxy the API so dev mode is same-origin.
    // UD_API_TARGET=http://127.0.0.1:<daemon-port> npm run dev
    proxy: {
      '/api': { target: process.env.UD_API_TARGET || 'http://127.0.0.1:20260' },
    },
  },
})
