// @vitest-environment node
//
// The vendored Codec2 module (web/public/vendor/codec2/) encodes and decodes
// in the same process the SPA will run it in: a generated 8 kHz tone goes
// through _codec2_encode at 700C, the frames come back through
// _codec2_decode, the frame counts match the mode table and the output is
// not silence. Every row of the mode table is checked against the wasm, so
// a rebuilt module with other mode ids or frame sizes fails here, not in a
// browser at a kit. [MESHSAT-1735]
import { test, expect } from 'vitest'
import { pathToFileURL } from 'node:url'
import { resolve } from 'node:path'
import {
  AM_CODEC2_700C, CODEC2_MODES, SAMPLE_RATE, bytesPerFrame, codec2Duration, decodeCodec2, encodeCodec2, loadCodec2
} from '@/utils/codec2'

const GLUE = pathToFileURL(resolve(__dirname, '../../public/vendor/codec2/codec2.js')).href

function tone(seconds, hz, amplitude = 0.5) {
  const n = Math.round(seconds * SAMPLE_RATE)
  const pcm = new Int16Array(n)
  for (let i = 0; i < n; i++) pcm[i] = Math.round(Math.sin(2 * Math.PI * hz * i / SAMPLE_RATE) * amplitude * 32767)
  return pcm
}

function rms(pcm) {
  let acc = 0
  for (let i = 0; i < pcm.length; i++) acc += pcm[i] * pcm[i]
  return Math.sqrt(acc / Math.max(1, pcm.length))
}

test('the mode table matches the module', async () => {
  const m = await loadCodec2(GLUE)
  for (const [mode, spec] of Object.entries(CODEC2_MODES)) {
    const st = m._codec2_create(spec.id)
    expect(st, `mode ${spec.label} opens`).not.toBe(0)
    expect(m._codec2_bits_per_frame(st), `${spec.label} bits per frame`).toBe(spec.bitsPerFrame)
    expect(m._codec2_samples_per_frame(st), `${spec.label} samples per frame`).toBe(spec.samplesPerFrame)
    m._codec2_destroy(st)
    expect(bytesPerFrame(Number(mode))).toBe((spec.bitsPerFrame + 7) >> 3)
  }
})

test('a 700C note goes through the module and comes back as sound', async () => {
  const m = await loadCodec2(GLUE)
  const seconds = 2
  const pcm = tone(seconds, 440)
  const frames = encodeCodec2(m, pcm, AM_CODEC2_700C)
  const spec = CODEC2_MODES[AM_CODEC2_700C]
  const expectFrames = Math.floor(pcm.length / spec.samplesPerFrame)
  expect(frames.length).toBe(expectFrames * 4) // 700C: 28 bits, 4 bytes, per 40 ms
  expect(frames.length).toBe(200) // 2 s at 100 B/s
  expect(codec2Duration(AM_CODEC2_700C, frames.length)).toBe(seconds)

  const out = decodeCodec2(m, frames, AM_CODEC2_700C)
  expect(out.frames).toBe(expectFrames)
  expect(out.bytesPerFrame).toBe(4)
  expect(out.samplesPerFrame).toBe(320)
  expect(out.pcm.length).toBe(pcm.length)
  const level = rms(out.pcm)
  expect(level, 'the decoded note is not silence').toBeGreaterThan(0.02)
  let peak = 0
  for (const s of out.pcm) peak = Math.max(peak, Math.abs(s))
  expect(peak, 'samples are scaled to [-1, 1]').toBeLessThanOrEqual(1)
})

test('a trailing partial frame is dropped, not padded', async () => {
  const m = await loadCodec2(GLUE)
  const frames = encodeCodec2(m, tone(1, 300), AM_CODEC2_700C)
  const ragged = new Uint8Array(frames.length + 3)
  ragged.set(frames)
  ragged.set([0xaa, 0xbb, 0xcc], frames.length)
  const whole = decodeCodec2(m, frames, AM_CODEC2_700C)
  const cut = decodeCodec2(m, ragged, AM_CODEC2_700C)
  expect(cut.frames).toBe(whole.frames)
  expect(cut.pcm.length).toBe(whole.pcm.length)
  expect(codec2Duration(AM_CODEC2_700C, ragged.length)).toBe(codec2Duration(AM_CODEC2_700C, frames.length))
})

test('silence encodes and decodes as near silence', async () => {
  const m = await loadCodec2(GLUE)
  const frames = encodeCodec2(m, new Int16Array(SAMPLE_RATE), AM_CODEC2_700C)
  const out = decodeCodec2(m, frames, AM_CODEC2_700C)
  expect(out.frames).toBe(25)
  expect(rms(out.pcm)).toBeLessThan(0.01)
})

test('an unsupported mode is refused before the module is touched', async () => {
  const m = await loadCodec2(GLUE)
  expect(() => decodeCodec2(m, new Uint8Array(8), 0x01)).toThrow(/450PWB/)
  expect(() => decodeCodec2(m, new Uint8Array(8), 0x10)).toThrow(/Opus/)
})
