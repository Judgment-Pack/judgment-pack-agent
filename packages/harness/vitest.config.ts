import { defineConfig } from 'vitest/config'

// The Desk's own test ground, carried with the harness: jsdom, one setup file
// that supplies the host, and the Desk's ceiling for a test that stands a whole
// session up under contention.
export default defineConfig({
  test: {
    environment: 'jsdom',
    setupFiles: ['testing/setup.ts'],
    include: ['**/*.test.ts'],
    exclude: ['node_modules/**', 'dist/**'],
    testTimeout: 20_000
  }
})
