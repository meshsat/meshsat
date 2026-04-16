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
      name: 'tesseract',
      use: { baseURL: 'http://nllei01tesseract01:6050' },
    },
    {
      name: 'parallax',
      use: { baseURL: 'http://nllei01parallax01:6050' },
    },
  ],
});
