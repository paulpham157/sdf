const { defineConfig } = require('@playwright/test');

module.exports = defineConfig({
  testDir: '.',
  testMatch: 'contrast.spec.js',
  timeout: 60_000,
  expect: { timeout: 20_000 },
  outputDir: 'test-results',
  use: {
    baseURL: 'http://127.0.0.1:8765',
    viewport: { width: 1440, height: 900 },
  },
  webServer: {
    command: 'python3 -m http.server 8765 --bind 127.0.0.1 --directory ../../site',
    url: 'http://127.0.0.1:8765/',
    reuseExistingServer: !process.env.CI,
    timeout: 30_000,
  },
});
