// The API client with a kit that asks for a token: the stored token goes
// out as the Bearer, a 401 opens the login overlay once, the right token
// is kept and the wrong one is not. [MESHSAT-1738]
import assert from 'node:assert/strict'
import { test, beforeEach } from 'vitest'
import api, { apiToken, setApiToken } from '@/api/client'
import ApiLogin from '@/components/ApiLogin.vue'
import { button, flush, input, json, mountRoot, serve, useHarness } from './harness.js'

useHarness()

beforeEach(async () => {
  // The client tells the page once per lock-out and a login re-arms it:
  // log in against an answering kit so every test starts armed and with
  // no token.
  serve(async () => new Response(null, { status: 204 }))
  await api.login('')
  setApiToken('')
})

function unauthorized() {
  return new Response(JSON.stringify({ error: 'api token required' }), { status: 401, headers: { 'Content-Type': 'application/json' } })
}

test('stored_token_goes_out_as_the_bearer_on_every_request', async () => {
  const seen = []
  serve(async (url, opts) => { seen.push([url, opts.headers?.Authorization || '']); return json([]) })
  await api.get('/presets')
  setApiToken('kit-token-for-the-tests')
  await api.get('/presets')
  await api.post('/messages/send', { text: 'x' })
  await api.put('/ttc/flow', { path: 'aprs' })
  assert.deepEqual(seen, [
    ['/api/presets', ''],
    ['/api/presets', 'Bearer kit-token-for-the-tests'],
    ['/api/messages/send', 'Bearer kit-token-for-the-tests'],
    ['/api/ttc/flow', 'Bearer kit-token-for-the-tests']
  ])
})

test('a_401_tells_the_page_once_and_the_error_says_why', async () => {
  let told = 0
  const onTold = () => told++
  window.addEventListener('meshsat:unauthorized', onTold)
  try {
    serve(async () => unauthorized())
    for (let i = 0; i < 3; i++) {
      await assert.rejects(api.get('/presets'), err => err.status === 401 && err.message === 'api token required')
    }
    assert.equal(told, 1)
    // A login with the right token re-arms it.
    serve(async (url) => url === '/api/auth/login' ? new Response(null, { status: 204 }) : unauthorized())
    assert.equal(await api.login('kit-token-for-the-tests'), true)
    await assert.rejects(api.get('/presets'))
    assert.equal(told, 2)
  } finally {
    window.removeEventListener('meshsat:unauthorized', onTold)
  }
})

test('login_hands_the_candidate_token_and_keeps_only_a_right_one', async () => {
  const seen = []
  serve(async (url, opts) => {
    seen.push([url, opts.method, opts.headers?.Authorization || ''])
    return opts.headers?.Authorization === 'Bearer right-token-for-the-tests' ? new Response(null, { status: 204 }) : unauthorized()
  })
  assert.equal(await api.login(' wrong-token-for-the-tests '), false)
  assert.equal(apiToken(), '')
  assert.equal(await api.login(' right-token-for-the-tests '), true)
  assert.equal(apiToken(), 'right-token-for-the-tests')
  assert.deepEqual(seen, [
    ['/api/auth/login', 'POST', 'Bearer wrong-token-for-the-tests'],
    ['/api/auth/login', 'POST', 'Bearer right-token-for-the-tests']
  ])
  await api.logout()
  assert.equal(apiToken(), '')
  assert.equal(seen[2][0], '/api/auth/logout')
})

test('the_overlay_opens_on_a_401_and_says_when_the_token_is_refused', async () => {
  serve(async () => unauthorized())
  mountRoot(ApiLogin)
  await flush()
  assert.equal(document.querySelector('[data-testid="api-login"]'), null)
  await assert.rejects(api.get('/presets'))
  await flush()
  assert.ok(document.querySelector('[data-testid="api-login"]'), 'overlay closed after a 401')
  assert.equal(button('Unlock').disabled, true)
  await input(document.querySelector('[data-testid="api-login-token"]'), 'wrong-token-for-the-tests')
  assert.equal(button('Unlock').disabled, false)
  button('Unlock').click()
  await flush()
  assert.ok(document.querySelector('[data-testid="api-login-refused"]'), 'no refusal shown')
  assert.equal(apiToken(), '')

  serve(async (url) => url === '/api/auth/login' ? new Response(null, { status: 204 }) : json([]))
  await input(document.querySelector('[data-testid="api-login-token"]'), 'right-token-for-the-tests')
  button('Unlock').click()
  await flush()
  assert.equal(apiToken(), 'right-token-for-the-tests')
  assert.equal(document.querySelector('[data-testid="api-login"]'), null, 'overlay still open after a login')
})
