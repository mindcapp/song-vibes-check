import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

export default defineConfig({
  plugins: [react()],
  server: {
    port: 5173,
    // The Go API has no CORS headers: in development the browser talks to
    // Vite, and Vite forwards /api/* to the API on the host.
    proxy: {
      '/api': 'http://localhost:8080',
    },
  },
  esbuild: {
    loader: 'jsx',
    include: /src\/.*\.jsx?$/,
  },
  optimizeDeps: {
    esbuildOptions: {
      loader: { '.js': 'jsx' },
    },
  },
})
