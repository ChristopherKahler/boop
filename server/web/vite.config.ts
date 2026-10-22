import { writeFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { defineConfig, type Plugin } from 'vitest/config'
import { svelte } from '@sveltejs/vite-plugin-svelte'

// emptyOutDir wipes the committed dist/.gitkeep on every build; put it back so
// a build never shows up as a deletion and a fresh checkout still compiles.
const keepGitkeep = (): Plugin => ({
  name: 'keep-gitkeep',
  apply: 'build',
  closeBundle() {
    writeFileSync(resolve(__dirname, '../internal/web/dist/.gitkeep'), '')
  },
})

// Built assets land in the Go module so `go:embed all:dist` picks them up.
export default defineConfig({
  plugins: [svelte(), keepGitkeep()],
  build: {
    outDir: '../internal/web/dist',
    emptyOutDir: true,
  },
  server: {
    port: 5173,
    proxy: {
      '/api': 'http://localhost:8080',
      '/health': 'http://localhost:8080',
    },
  },
  test: {
    environment: 'jsdom',
    include: ['src/**/*.test.ts'],
  },
})
