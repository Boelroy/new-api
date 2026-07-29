import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

export default defineConfig({
  // Relative base so hashed assets emit as ./assets/... and resolve against
  // the runtime-injected <base href> — lets the same build serve at root or
  // under any prefix (e.g. /dashboard/) with no rebuild.
  base: './',
  plugins: [react()],
  server: {
    proxy: {
      '/api': 'http://localhost:8090',
      '/login': 'http://localhost:8090',
      '/logout': 'http://localhost:8090',
    },
  },
  build: {
    outDir: 'dist',
    emptyOutDir: true,
  },
})
