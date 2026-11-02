import tailwindcss from '@tailwindcss/vite'
import react from '@vitejs/plugin-react'
import { defineConfig } from 'vite'

const billingBasePath = `${(process.env.BILLING_BASE_PATH || '/').replace(/\/+$/, '')}/`

export default defineConfig({
  base: billingBasePath,
  plugins: [react(), tailwindcss()],
  server: {
    port: 5176,
    proxy: {
      [`${billingBasePath}api`]: {
        target: 'http://localhost:8092',
        rewrite: (path) => path.slice(billingBasePath.length - 1),
      },
    },
  },
  build: {
    outDir: 'dist-billing',
    rollupOptions: { input: 'billing.html' },
  },
})
