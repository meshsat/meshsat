import { defineConfig } from '@playwright/test';

export default defineConfig({
  testDir: '.',
  timeout: 30000,
  use: {
    headless: true,
    screenshot: 'only-on-failure',
  },
  projects: [
    {
      name: 'mule01',
      use: { baseURL: 'http://nllei01mule01-wireless:6050' },
    },
    {
      name: 'pifour01',
      use: { baseURL: 'http://nllei01pifour01-wireless:6050' },
    },
    {
      name: 'bananapi01',
      use: { baseURL: 'http://nllei01bananapi01-wireless:6050' },
    },
  ],
});
