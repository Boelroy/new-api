import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';

// V2 SPA is served under /v2/ by the Go binary. base='/v2/' makes Vite
// emit asset URLs prefixed with /v2/assets/... so hashed JS/CSS resolve
// correctly through the embed handler in frontend_v2.go.
export default defineConfig({
  base: '/v2/',
  plugins: [react()],
  server: {
    port: 5174,
    proxy: {
      '/api': 'http://localhost:8090',
    },
  },
});
