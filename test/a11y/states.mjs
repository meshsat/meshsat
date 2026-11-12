// What the accessibility run measures: three ways the app is used, the
// screens SIDN deliverable D5 names (Dashboard, Messages with Inbox and
// Compose, Map, Settings), and the states a person reaches on each.

// Timers that would change the page while it is measured are switched off.
const QUIET = 'saverMs=0&bundleWatchMs=0&bundleFreshMs=0'

export const VARIANTS = [
  // What the field kits run: the 7-inch panel, operator shell.
  { id: 'kiosk-operator', shell: 'operator', query: `kiosk=1&shell=operator&${QUIET}`,
    context: { viewport: { width: 853, height: 480 }, deviceScaleFactor: 1.5, hasTouch: true } },
  // Someone taps ENG on the panel.
  { id: 'kiosk-engineer', shell: 'engineer', query: `kiosk=1&shell=engineer&${QUIET}`,
    context: { viewport: { width: 853, height: 480 }, deviceScaleFactor: 1.5, hasTouch: true } },
  // A laptop on the kit's network.
  { id: 'desktop-engineer', shell: 'engineer', query: `shell=engineer&${QUIET}`,
    context: { viewport: { width: 1440, height: 900 }, deviceScaleFactor: 1 } }
]

export const PROFILES = ['rich', 'empty']

// Settings tabs per shell (SettingsView.vue `allTabs`).
const SETTINGS_OPERATOR = ['radio', 'channels', 'position', 'iridium', 'cellular', 'cell', 'tetra']
const SETTINGS_ENGINEER = [...SETTINGS_OPERATOR, 'canned', 'mqtt', 'device_mqtt', 'zigbee', 'store_forward',
  'range_test', 'deadman', 'tak', 'aprs', 'credentials', 'remote_mgmt', 'network', 'routing', 'media_hop',
  'config_mgmt', 'about', 'devices']

const MESSAGES_TABS = [['mesh', 'Mesh Messages'], ['sbd', 'SBD Queue'], ['sms', 'SMS'], ['broadcasts', 'Broadcasts'], ['webhooks', 'Webhooks']]

async function click (page, locator) {
  const el = typeof locator === 'string' ? page.locator(locator).first() : locator.first()
  await el.waitFor({ state: 'visible', timeout: 5000 })
  await el.click()
}

// Each state: `view` and `sub` make its key; `path` is opened; `act` brings
// the page into the state; `scope` is `shell` (header and bottom nav only)
// or `view` (everything else); `rich` states need data to be reachable.
export function statesFor (variant, profile) {
  const op = variant.shell === 'operator'
  const out = []
  const add = (s) => { if (!s.rich || profile === 'rich') out.push({ scope: 'view', ...s }) }

  add({ view: 'shell', sub: 'header', path: '/', scope: 'shell' })
  add({ view: 'shell', sub: 'power', path: '/', scope: 'shell', act: (p) => click(p, 'header button.power-chip') })
  if (op) add({ view: 'shell', sub: 'more', path: '/', scope: 'shell', act: (p) => click(p, 'header nav button[aria-label="More"]') })

  add({ view: 'dashboard', sub: 'main', path: '/' })
  if (!op) add({ view: 'dashboard', sub: 'customize', path: '/', act: (p) => click(p, '[data-testid="dash-customize"]') })

  for (const [key, label] of MESSAGES_TABS) {
    add({ view: 'messages', sub: key, path: '/messages',
      act: key === 'mesh' ? null : (p) => click(p, p.locator('main button', { hasText: label })) })
  }

  add({ view: 'inbox', sub: 'main', path: '/inbox' })
  add({ view: 'compose', sub: 'main', path: '/compose' })
  add({ view: 'compose', sub: 'picker', path: '/compose', act: async (p) => { await click(p, '#contact-picker input'); await p.keyboard.type('S') } })

  add({ view: 'map', sub: 'main', path: '/map' })
  add({ view: 'map', sub: 'popup', path: '/map', rich: true, act: (p) => click(p, '.leaflet-marker-icon') })
  add({ view: 'map', sub: 'geofence', path: '/map', act: (p) => click(p, p.locator('main div', { hasText: /^Geofence Zones/ }).getByRole('button', { name: 'Manage', exact: true })) })

  for (const tab of op ? SETTINGS_OPERATOR : SETTINGS_ENGINEER) {
    add({ view: 'settings', sub: tab, path: `/settings?tab=${tab}` })
  }
  return out
}

export const stateKey = (variant, profile, s) => `${variant.id}/${profile}/${s.view}:${s.sub}`
