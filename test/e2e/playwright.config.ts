import { defineConfig } from '@playwright/test';
import { execFileSync } from 'child_process';
import { existsSync } from 'fs';

// A kit answers on its house name (nllei01<kit>01) or, when it sits behind the Mudi
// 5G router, on its -field name through the WireGuard tunnel. Which one depends on
// the access point the kit joined, not on where it is, so resolve at config load:
// an explicit MESHSAT_<KIT>_URL wins, then scripts/kit-host.sh, then the house name.
// [MESHSAT-1182]
function kitURL(kit: string): string {
  const override = process.env[`MESHSAT_${kit.toUpperCase()}_URL`];
  if (override) return override;

  const script = ['scripts/kit-host.sh', '../../scripts/kit-host.sh'].find(existsSync);
  if (script) {
    try {
      const host = execFileSync('bash', [script, kit], { encoding: 'utf8', timeout: 15000 }).trim();
      if (host) return `http://${host}:6050`;
    } catch {
      // Neither name answered; fall through so the failure surfaces in the test run.
    }
  }
  return `http://nllei01${kit}01:6050`;
}

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
      use: { baseURL: kitURL('tesseract') },
    },
    {
      name: 'parallax',
      use: { baseURL: kitURL('parallax') },
    },
  ],
});
