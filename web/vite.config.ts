import { mkdirSync, rmSync, writeFileSync } from 'node:fs'
import { resolve } from 'node:path'
import react from '@vitejs/plugin-react'
import { defineConfig } from 'vitest/config'

const outDir = resolve(__dirname, '../internal/ui/dist')

const daemon = { target: 'http://127.0.0.1:7777', changeOrigin: true, headers: { origin: 'http://127.0.0.1:7777' } }

export default defineConfig({
  base: '/ui/',
  plugins: [
    react(),
    {
      // Vite's own emptyOutDir would also delete .gitkeep, the embed anchor that
      // keeps go:embed valid in a plain checkout, and a failed build would leave
      // it gone. So we clear the previous build ourselves and never touch .gitkeep.
      name: 'agentbox-keep-gitkeep',
      buildStart() {
        mkdirSync(outDir, { recursive: true })
        rmSync(resolve(outDir, 'assets'), { recursive: true, force: true })
        rmSync(resolve(outDir, 'index.html'), { force: true })
        writeFileSync(resolve(outDir, '.gitkeep'), '')
      },
      closeBundle() {
        writeFileSync(resolve(outDir, '.gitkeep'), '')
      },
    },
  ],
  build: { outDir, emptyOutDir: false },
  server: {
    // Cookies are host-only on 127.0.0.1 and ignore the port, so proxying
    // the API from the Vite dev server keeps the session working.
    proxy: {
      '/v1': daemon,
      '/ui/login': daemon,
      '/ui/logout': daemon,
    },
  },
  test: { environment: 'node', include: ['src/**/*.test.ts'] },
})
