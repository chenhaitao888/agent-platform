import react from '@vitejs/plugin-react'
import { loadEnv } from 'vite'
import { defineConfig } from 'vitest/config'
import { loopbackAPITarget } from './dev-proxy.ts'

export default defineConfig(({ mode }) => {
  const target = loopbackAPITarget(loadEnv(mode, '.', 'AGENT_PLATFORM_DEV_API_URL').AGENT_PLATFORM_DEV_API_URL)
  return {
    plugins: [react()],
    server: {
      host: '127.0.0.1',
      proxy: {
        '/api/healthz': {
          target,
          changeOrigin: true,
          rewrite: (path) => path.replace(/^\/api/, ''),
        },
        '/api/v1': {
          target,
          changeOrigin: true,
        },
      },
    },
    preview: { host: '127.0.0.1' },
    test: {
      environment: 'jsdom',
      setupFiles: './src/test/setup.ts',
    },
  }
})
