/**
 * Playwright E2E tests for MeshSat Hub auth UI.
 *
 * Run with auth disabled (HUB_AUTH_ENABLED=false) to test the SPA
 * renders correctly and API key management works.
 *
 * Usage:
 *   HUB_URL=http://localhost:6050 npx playwright test test/e2e/hub-auth.spec.ts
 */
import { test, expect } from '@playwright/test'

const HUB_URL = process.env.HUB_URL || 'http://localhost:6050'

test.describe('Hub Auth UI', () => {
  test('dashboard loads and shows nav tabs', async ({ page }) => {
    await page.goto(HUB_URL)
    await expect(page.locator('text=MeshSat')).toBeVisible()
    await expect(page.locator('text=Dashboard')).toBeVisible()
    await expect(page.locator('text=Comms')).toBeVisible()
    await expect(page.locator('text=Settings')).toBeVisible()
  })

  test('auth status shows disabled', async ({ page }) => {
    const res = await page.request.get(`${HUB_URL}/auth/status`)
    expect(res.ok()).toBeTruthy()
    const data = await res.json()
    expect(data.enabled).toBe(false)
  })

  test('API key page loads', async ({ page }) => {
    await page.goto(`${HUB_URL}/keys`)
    await expect(page.locator('text=API Keys')).toBeVisible()
    await expect(page.locator('text=Create API Key')).toBeVisible()
  })

  test('create and revoke API key', async ({ page }) => {
    await page.goto(`${HUB_URL}/keys`)

    // Fill create form
    await page.fill('input[placeholder*="Field Device"]', 'Playwright Test Key')
    await page.selectOption('select', 'operator')
    await page.click('button:has-text("Create")')

    // Verify key was created and plaintext is shown
    await expect(page.locator('text=copy it now')).toBeVisible({ timeout: 5000 })
    await expect(page.locator('code')).toContainText('meshsat_')

    // Verify key appears in the table
    await expect(page.locator('text=Playwright Test Key')).toBeVisible()
    await expect(page.locator('text=operator')).toBeVisible()

    // Revoke the key
    page.on('dialog', dialog => dialog.accept()) // handle confirm dialog
    await page.click('button:has-text("Revoke")')

    // Verify key is gone
    await expect(page.locator('text=Playwright Test Key')).not.toBeVisible({ timeout: 3000 })
  })

  test('device CRUD via API', async ({ page }) => {
    // Create
    const createRes = await page.request.post(`${HUB_URL}/api/device-registry`, {
      data: { imei: '300234063904191', label: 'PW Test Device', type: 'rockblock' }
    })
    expect(createRes.ok()).toBeTruthy()
    const device = await createRes.json()
    expect(device.imei).toBe('300234063904191')

    // Read
    const getRes = await page.request.get(`${HUB_URL}/api/device-registry/${device.id}`)
    expect(getRes.ok()).toBeTruthy()

    // Delete
    const delRes = await page.request.delete(`${HUB_URL}/api/device-registry/${device.id}`)
    expect(delRes.status()).toBe(204)

    // Verify gone
    const gone = await page.request.get(`${HUB_URL}/api/device-registry/${device.id}`)
    expect(gone.status()).toBe(404)
  })

  test('API key auth works for API requests', async ({ page }) => {
    // Create a key
    const keyRes = await page.request.post(`${HUB_URL}/api/auth/keys`, {
      data: { label: 'PW Auth Test', role: 'viewer' }
    })
    const keyData = await keyRes.json()
    const apiKey = keyData.key

    // Use it for an authenticated request
    const devRes = await page.request.get(`${HUB_URL}/api/device-registry`, {
      headers: { 'Authorization': `Bearer ${apiKey}` }
    })
    expect(devRes.ok()).toBeTruthy()

    // Revoke
    await page.request.delete(`${HUB_URL}/api/auth/keys/${keyData.id}`)
  })
})
