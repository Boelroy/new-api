import { defineConfig } from 'vitest/config'
import react from '@vitejs/plugin-react'
export default defineConfig({
  plugins: [react()],
  resolve: { dedupe: ['react-router-dom'] },
  test: {
    environment: 'jsdom',
    setupFiles: ['./src/test-setup.ts'],
    include: ['src/**/__tests__/*.test.{ts,tsx}', 'tests/*.test.tsx'],
    exclude: ['src/**/*.browser.test.ts'],
  },
})
