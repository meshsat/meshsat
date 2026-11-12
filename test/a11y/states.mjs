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

// Settings tabs per shell (SettingsView.vue `allTabs`), with their labels.
const SETTINGS = [['radio', 'Radio'], ['channels', 'Channels'], ['position', 'Position'], ['iridium', 'Satellite'],
  ['cellular', 'Cellular'], ['cell', '2G Cell'], ['tetra', 'TETRA']]
const SETTINGS_ENGINEER = [...SETTINGS, ['canned', 'Canned Msg'], ['mqtt', 'MQTT'], ['device_mqtt', 'Device MQTT'],
  ['zigbee', 'ZigBee'], ['store_forward', 'S&F'], ['range_test', 'Range Test'], ['deadman', 'Dead Man'], ['tak', 'TAK'],
  ['aprs', 'APRS'], ['credentials', 'Credentials'], ['remote_mgmt', 'Remote Mgmt'], ['network', 'Network'],
  ['routing', 'Routing'], ['media_hop', 'Media Hopping'], ['config_mgmt', 'Export/Import'], ['about', 'About'], ['devices', 'Devices']]

const MESSAGES_TABS = [['mesh', 'Mesh Messages'], ['sbd', 'SBD Queue'], ['sms', 'SMS'], ['broadcasts', 'Broadcasts'], ['webhooks', 'Webhooks']]

// The "+ Add" button beside a section heading in Settings.
const addBeside = (heading) => `xpath=//h4[normalize-space()="${heading}"]/following-sibling::button[1]`

async function click (page, locator) {
  const el = typeof locator === 'string' ? page.locator(locator).first() : locator.first()
  await el.waitFor({ state: 'visible', timeout: 5000 })
  await el.click()
}

// Proof that a state is what its name says: the element must be visible.
const shows = (selector) => async (page) => {
  await (typeof selector === 'string' ? page.locator(selector) : selector(page)).first().waitFor({ state: 'visible', timeout: 5000 })
}
// The tab button whose text starts with this label carries the active class.
const activeTab = (label, cls) => async (page) => {
  const esc = label.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
  const b = page.locator('main button').filter({ hasText: new RegExp(`^\\s*${esc}(\\s|$)`) }).first()
  await b.waitFor({ state: 'visible', timeout: 5000 })
  const c = await b.getAttribute('class')
  if (!c || !c.split(/\s+/).includes(cls)) throw new Error(`tab "${label}" is not the active one`)
}

// Each state: `view` and `sub` make its key; `path` is opened; `act` brings
// the page into the state; `expect` must then hold, or the state errors;
// `scope` is `shell` (header and bottom nav only) or `view` (everything
// else); `rich` states need data to be reachable.
export function statesFor (variant, profile) {
  const op = variant.shell === 'operator'
  const out = []
  const add = (s) => { if (!s.rich || profile === 'rich') out.push({ scope: 'view', ...s }) }

  add({ view: 'shell', sub: 'header', path: '/', scope: 'shell', expect: shows('header nav') })
  add({ view: 'shell', sub: 'power', path: '/', scope: 'shell', act: (p) => click(p, 'header button.power-chip'),
    expect: shows('header button.power-chip[aria-expanded="true"]') })
  if (op) {
    add({ view: 'shell', sub: 'more', path: '/', scope: 'shell', act: (p) => click(p, 'header nav button[aria-label="More"]'),
      expect: shows('#meshsat-nav-more-menu') })
  }

  add({ view: 'dashboard', sub: 'main', path: '/', expect: shows(op ? 'main :text("Mission State")' : '[data-testid="dash-customize"]') })
  if (!op) {
    add({ view: 'dashboard', sub: 'customize', path: '/', act: (p) => click(p, '[data-testid="dash-customize"]'),
      expect: shows('[data-testid="dash-customize"][aria-pressed="true"]') })
    add({ view: 'dashboard', sub: 'stats', path: '/', act: (p) => click(p, 'main h2.cursor-pointer'),
      expect: shows('[aria-label="Widget diagnostics"]') })
    add({ view: 'dashboard', sub: 'node', path: '/', rich: true,
      act: (p) => click(p, 'main div.cursor-pointer:has(> span.truncate)'),
      expect: shows('[aria-label="Raw node data"]') })
  }

  for (const [key, label] of MESSAGES_TABS) {
    add({ view: 'messages', sub: key, path: '/messages',
      act: key === 'mesh' ? null : (p) => click(p, p.locator('main button', { hasText: label })),
      expect: activeTab(label, 'text-teal-400') })
  }
  add({ view: 'messages', sub: 'deliveries', path: '/messages', rich: true,
    act: (p) => click(p, p.locator('main button', { hasText: 'Deliveries' })),
    expect: shows('div.fixed.inset-0 button[aria-label="Close"]') })
  add({ view: 'messages', sub: 'compose', path: '/messages', act: (p) => click(p, p.locator('main button', { hasText: '+ Message' })),
    expect: shows('input[aria-label="Message text"]') })
  const presets = (p) => click(p, p.locator('main button[title="Quick presets"]'))
  add({ view: 'messages', sub: 'presets', path: '/messages', act: presets, expect: shows((p) => p.locator('main button', { hasText: '+ New Preset' })) })
  add({ view: 'messages', sub: 'preset-edit', path: '/messages',
    act: async (p) => { await presets(p); await click(p, p.locator('main button', { hasText: '+ New Preset' })) },
    expect: shows('#msg-presetform-name') })
  add({ view: 'messages', sub: 'sms-thread', path: '/messages', rich: true,
    act: async (p) => { await click(p, p.locator('main button', { hasText: 'SMS' })); await click(p, 'main div.cursor-pointer:has(> div.rounded-full)') },
    expect: shows('input[aria-label="Reply text"]') })

  add({ view: 'inbox', sub: 'main', path: '/inbox', expect: shows('main a[href="/compose"]') })
  add({ view: 'compose', sub: 'main', path: '/compose', expect: shows('#contact-picker input') })
  add({ view: 'compose', sub: 'picker', path: '/compose', act: async (p) => { await click(p, '#contact-picker input'); await p.keyboard.type('S') },
    expect: profile === 'empty' ? shows('#contact-picker input') : shows('#contact-picker ul[role="listbox"]') })

  add({ view: 'map', sub: 'main', path: '/map', expect: shows('.leaflet-container') })
  add({ view: 'map', sub: 'popup', path: '/map', rich: true, act: (p) => click(p, '.leaflet-marker-icon'), expect: shows('.leaflet-popup') })
  const manage = (p) => click(p, p.locator('main div', { hasText: /^Geofence Zones/ }).getByRole('button', { name: 'Manage', exact: true }))
  add({ view: 'map', sub: 'geofence', path: '/map', act: manage, expect: shows((p) => p.locator('main button', { hasText: '+ New Zone' })) })
  add({ view: 'map', sub: 'new-zone', path: '/map', act: async (p) => { await manage(p); await click(p, p.locator('main button', { hasText: '+ New Zone' })) },
    expect: shows('input[aria-label="Zone name"]') })

  for (const [tab, label] of op ? SETTINGS : SETTINGS_ENGINEER) {
    add({ view: 'settings', sub: tab, path: `/settings?tab=${tab}`, expect: activeTab(label, 'bg-teal-600') })
  }
  // Forms that open from a button inside a tab.
  add({ view: 'settings', sub: 'cellular-sim', path: '/settings?tab=cellular', act: (p) => click(p, addBeside('SIM Cards')),
    expect: shows('#set-simform-iccid') })
  add({ view: 'settings', sub: 'cellular-contact', path: '/settings?tab=cellular', act: (p) => click(p, addBeside('SMS Contacts')),
    expect: shows('#set-contactform-name') })
  if (!op) {
    add({ view: 'settings', sub: 'remote_mgmt-peer', path: '/settings?tab=remote_mgmt', act: (p) => click(p, addBeside('Peers')),
      expect: shows('[id^="set-oobpeerform-addresses-"]') })
  }
  return out
}

export const stateKey = (variant, profile, s) => `${variant.id}/${profile}/${s.view}:${s.sub}`
