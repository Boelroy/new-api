import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';
import tailwindcss from '@tailwindcss/vite';

// V3 SPA ("NewApi Dash") is served under /v3/ by the Go binary. base='/v3/'
// makes Vite emit asset URLs prefixed with /v3/assets/... so hashed JS/CSS
// resolve correctly through the embed handler in frontend_v3.go. API calls go
// to /api/* at the service root (see src/basePath.ts), NOT under /v3.
export default defineConfig({
  base: '/v3/',
  plugins: [react(), tailwindcss()],
  server: {
    port: 5175,
    proxy: {
      '/api': 'http://localhost:8090',
    },
  },
});
