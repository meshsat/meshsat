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

// The "+ Add" button beside a section heading in Settings.
const addBeside = (heading) => `xpath=//h4[normalize-space()="${heading}"]/following-sibling::button[1]`

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
  if (!op) {
    add({ view: 'dashboard', sub: 'customize', path: '/', act: (p) => click(p, '[data-testid="dash-customize"]') })
    add({ view: 'dashboard', sub: 'stats', path: '/', act: (p) => click(p, 'main h2.cursor-pointer') })
    add({ view: 'dashboard', sub: 'node', path: '/', rich: true, act: (p) => click(p, p.locator('main div.cursor-pointer', { hasText: 'Sample Long 27' })) })
  }

  for (const [key, label] of MESSAGES_TABS) {
    add({ view: 'messages', sub: key, path: '/messages',
      act: key === 'mesh' ? null : (p) => click(p, p.locator('main button', { hasText: label })) })
  }

  add({ view: 'messages', sub: 'deliveries', path: '/messages', rich: true, act: (p) => click(p, p.locator('main button', { hasText: 'Deliveries' })) })
  add({ view: 'messages', sub: 'compose', path: '/messages', act: (p) => click(p, p.locator('main button', { hasText: '+ Message' })) })
  const presets = (p) => click(p, p.locator('main button[title="Quick presets"]'))
  add({ view: 'messages', sub: 'presets', path: '/messages', act: presets })
  add({ view: 'messages', sub: 'preset-edit', path: '/messages', act: async (p) => { await presets(p); await click(p, p.locator('main button', { hasText: '+ New Preset' })) } })
  add({ view: 'messages', sub: 'sms-thread', path: '/messages', rich: true,
    act: async (p) => { await click(p, p.locator('main button', { hasText: 'SMS' })); await click(p, 'main div.cursor-pointer') } })

  add({ view: 'inbox', sub: 'main', path: '/inbox' })
  add({ view: 'compose', sub: 'main', path: '/compose' })
  add({ view: 'compose', sub: 'picker', path: '/compose', act: async (p) => { await click(p, '#contact-picker input'); await p.keyboard.type('S') } })

  add({ view: 'map', sub: 'main', path: '/map' })
  add({ view: 'map', sub: 'popup', path: '/map', rich: true, act: (p) => click(p, '.leaflet-marker-icon') })
  const manage = (p) => click(p, p.locator('main div', { hasText: /^Geofence Zones/ }).getByRole('button', { name: 'Manage', exact: true }))
  add({ view: 'map', sub: 'geofence', path: '/map', act: manage })
  add({ view: 'map', sub: 'new-zone', path: '/map', act: async (p) => { await manage(p); await click(p, p.locator('main button', { hasText: '+ New Zone' })) } })

  for (const tab of op ? SETTINGS_OPERATOR : SETTINGS_ENGINEER) {
    add({ view: 'settings', sub: tab, path: `/settings?tab=${tab}` })
  }
  // Forms that open from a button inside a tab.
  add({ view: 'settings', sub: 'cellular-sim', path: '/settings?tab=cellular', act: (p) => click(p, addBeside('SIM Cards')) })
  add({ view: 'settings', sub: 'cellular-contact', path: '/settings?tab=cellular', act: (p) => click(p, addBeside('SMS Contacts')) })
  if (!op) add({ view: 'settings', sub: 'remote_mgmt-peer', path: '/settings?tab=remote_mgmt', act: (p) => click(p, addBeside('Peers')) })
  return out
}

export const stateKey = (variant, profile, s) => `${variant.id}/${profile}/${s.view}:${s.sub}`
