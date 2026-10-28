// Codec2 in the browser, for the voice notes an LXMF message carries
// (FIELD_AUDIO = [mode, bytes]). The decoder is libcodec2 as wasm, vendored
// under /vendor/codec2/ with its licence note, and loaded on first use so it
// never rides in the main bundle. The LXMF mode byte names the codec2 mode;
// the bytes are whole frames and nothing else (no header, no length, no mode
// inside), the last partial frame is dropped, as Sideband and MeshChat do.
// [MESHSAT-1735]

// The LXMF audio modes (LXMF.py): Codec2 0x01-0x09, Opus 0x10-0x19, custom
// 0xFF. 450 and 450PWB have no bindings in the clients either.
export const AM_CODEC2_450PWB = 0x01
export const AM_CODEC2_450 = 0x02
export const AM_CODEC2_700C = 0x03
export const AM_OPUS_OGG = 0x10
export const AM_OPUS_LAST = 0x19

export const SAMPLE_RATE = 8000

// LXMF mode byte -> codec2.h mode id, name, and the frame geometry
// (bits per frame, samples per frame) from codec2.c. The geometry lets a
// note's duration be read from its byte count before anything is decoded;
// the roundtrip test checks every row against the wasm.
export const CODEC2_MODES = Object.freeze({
  0x03: { id: 8, label: '700C', bitsPerFrame: 28, samplesPerFrame: 320 },
  0x04: { id: 5, label: '1200', bitsPerFrame: 48, samplesPerFrame: 320 },
  0x05: { id: 4, label: '1300', bitsPerFrame: 52, samplesPerFrame: 320 },
  0x06: { id: 3, label: '1400', bitsPerFrame: 56, samplesPerFrame: 320 },
  0x07: { id: 2, label: '1600', bitsPerFrame: 64, samplesPerFrame: 320 },
  0x08: { id: 1, label: '2400', bitsPerFrame: 48, samplesPerFrame: 160 },
  0x09: { id: 0, label: '3200', bitsPerFrame: 64, samplesPerFrame: 160 }
})

export function isCodec2(mode) { return Object.prototype.hasOwnProperty.call(CODEC2_MODES, mode) }
export function isOpus(mode) { return mode >= AM_OPUS_OGG && mode <= AM_OPUS_LAST }

// audioModeLabel names a mode the way the kit's previews do.
export function audioModeLabel(mode) {
  if (isCodec2(mode)) return `Codec2 ${CODEC2_MODES[mode].label}`
  if (mode === AM_CODEC2_450PWB) return 'Codec2 450PWB'
  if (mode === AM_CODEC2_450) return 'Codec2 450'
  if (isOpus(mode)) return 'Opus'
  if (mode === 0xff) return 'custom audio'
  return `audio mode ${mode}`
}

export function bytesPerFrame(mode) {
  const m = CODEC2_MODES[mode]
  return m ? (m.bitsPerFrame + 7) >> 3 : 0
}

// codec2Duration is the seconds of speech in `size` bytes of a Codec2 note,
// whole frames only; 0 for any other mode.
export function codec2Duration(mode, size) {
  const m = CODEC2_MODES[mode]
  if (!m || !size) return 0
  return Math.floor(size / bytesPerFrame(mode)) * m.samplesPerFrame / SAMPLE_RATE
}

export const CODEC2_URL = '/vendor/codec2/codec2.js'

let modulePromise = null

// loadCodec2 fetches and instantiates the module once; a failed load is
// forgotten so the next play tries again. `url` is for tests.
export function loadCodec2(url = CODEC2_URL) {
  if (!modulePromise) {
    modulePromise = import(/* @vite-ignore */ url)
      .then(glue => glue.default())
      .catch(err => { modulePromise = null; throw err })
  }
  return modulePromise
}

// decodeCodec2 turns a note's frames into mono PCM at 8 kHz in [-1, 1).
// Returns { pcm, frames, samplesPerFrame, bytesPerFrame }.
export function decodeCodec2(m, bytes, mode) {
  const spec = CODEC2_MODES[mode]
  if (!spec) throw new Error(`${audioModeLabel(mode)} is not supported`)
  const st = m._codec2_create(spec.id)
  if (!st) throw new Error(`codec2 mode ${spec.label} did not open`)
  try {
    const spf = m._codec2_samples_per_frame(st)
    const bpf = (m._codec2_bits_per_frame(st) + 7) >> 3
    const n = Math.floor(bytes.length / bpf)
    const pBits = m._malloc(bpf)
    const pPcm = m._malloc(spf * 2)
    try {
      const pcm = new Float32Array(n * spf)
      for (let f = 0; f < n; f++) {
        // The heap views are read through the module each time: a growth
        // replaces them.
        m.HEAPU8.set(bytes.subarray(f * bpf, (f + 1) * bpf), pBits)
        m._codec2_decode(st, pPcm, pBits)
        const s = m.HEAP16.subarray(pPcm >> 1, (pPcm >> 1) + spf)
        for (let i = 0; i < spf; i++) pcm[f * spf + i] = s[i] / 32768
      }
      return { pcm, frames: n, samplesPerFrame: spf, bytesPerFrame: bpf }
    } finally {
      m._free(pBits)
      m._free(pPcm)
    }
  } finally {
    m._codec2_destroy(st)
  }
}

// encodeCodec2 is the other direction, for the roundtrip test and for a
// recorder later: Int16 PCM at 8 kHz in, whole frames out, the tail that
// does not fill a frame left behind.
export function encodeCodec2(m, pcm16, mode) {
  const spec = CODEC2_MODES[mode]
  if (!spec) throw new Error(`${audioModeLabel(mode)} is not supported`)
  const st = m._codec2_create(spec.id)
  if (!st) throw new Error(`codec2 mode ${spec.label} did not open`)
  try {
    const spf = m._codec2_samples_per_frame(st)
    const bpf = (m._codec2_bits_per_frame(st) + 7) >> 3
    const n = Math.floor(pcm16.length / spf)
    const pBits = m._malloc(bpf)
    const pPcm = m._malloc(spf * 2)
    try {
      const out = new Uint8Array(n * bpf)
      for (let f = 0; f < n; f++) {
        m.HEAP16.set(pcm16.subarray(f * spf, (f + 1) * spf), pPcm >> 1)
        m._codec2_encode(st, pBits, pPcm)
        out.set(m.HEAPU8.subarray(pBits, pBits + bpf), f * bpf)
      }
      return out
    } finally {
      m._free(pBits)
      m._free(pPcm)
    }
  } finally {
    m._codec2_destroy(st)
  }
}

// formatClock renders seconds as m:ss for the duration label.
export function formatClock(seconds) {
  const s = Math.max(0, Math.round(seconds || 0))
  return `${Math.floor(s / 60)}:${String(s % 60).padStart(2, '0')}`
}
