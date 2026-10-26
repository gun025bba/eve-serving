// @ts-check
const { defineConfig, devices } = require('@playwright/test');
const { BASE_URL } = require('./env');
module.exports = defineConfig({
  testDir: './specs',
  timeout: 180_000,
  expect: { timeout: 30_000 },
  fullyParallel: false,
  workers: 1,                       // gateway max_concurrency is small; keep load gentle
  retries: 0,
  reporter: [['list'], ['html', { open: 'never' }]],
  use: { baseURL: BASE_URL, trace: 'retain-on-failure', screenshot: 'only-on-failure' },
  projects: [
    { name: 'api', testMatch: /api-.*\.spec\.js/ },
    { name: 'iphone-webkit', testMatch: /ui-.*\.spec\.js/, use: { ...devices['iPhone 15'] } },
  ],
});
