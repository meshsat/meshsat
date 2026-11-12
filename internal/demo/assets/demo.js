// The public demo (internal/demo, MESHSAT-1845). The demo answers anything
// but a read with 405 and the X-MeshSat-Demo header; this turns that into a
// short notice, so Send, Save or SOS never fail without a word. Loaded
// before the web app, which is the release bundle, unchanged.
(function () {
  'use strict'
  // The dark map's tiles (Carto) now answer a request without an API key
  // with a watermark, so the demo opens the map on the OpenStreetMap layer
  // (the app's own "Light" choice). A visitor's own choice is kept.
  try {
    if (!window.localStorage.getItem('meshsat-map-theme')) window.localStorage.setItem('meshsat-map-theme', 'light')
  } catch (e) { /* no storage: the app's default applies */ }
  var timer = null
  function notice () {
    var el = document.getElementById('meshsat-demo-toast')
    if (!el) {
      el = document.createElement('div')
      el.id = 'meshsat-demo-toast'
      el.setAttribute('role', 'status')
      el.setAttribute('aria-live', 'polite')
      document.body.appendChild(el)
    }
    el.textContent = 'Demo: nothing was sent or saved. This Bridge has no radios.'
    el.hidden = false
    clearTimeout(timer)
    timer = setTimeout(function () { el.hidden = true }, 4000)
  }
  var original = window.fetch
  if (typeof original !== 'function') return
  window.fetch = function () {
    return original.apply(this, arguments).then(function (res) {
      try {
        if (res && res.status === 405 && res.headers.get('X-MeshSat-Demo')) notice()
      } catch (e) { /* a notice is never worth breaking the app */ }
      return res
    })
  }
})()
