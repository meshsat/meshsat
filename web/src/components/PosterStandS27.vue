<script setup>
// Screensaver poster two: the owner's sentence over a plan of the TTC26 hall
// with our stand lit. [MESHSAT-1154 / MESHSAT-826]
//
// It is the second slide of KioskScreensaver, NOT a replacement for the bears
// poster (owner, 17 Sep 2026) — the pair is deliberate: one warm and funny,
// one sharp and factual, and they alternate.
//
// Drawn rather than exported as a PNG: 3 KB against the bears poster's 1.4 MB,
// sharp at any panel size, and the beacon on S27 can actually pulse. Type and
// colour follow the brand guide (Space Black ground, Signal Orange reserved for
// one object, IBM Plex Sans over IBM Plex Mono). The lockup is the approved
// master PNG placed unmodified — brand guide section 7 forbids redrawing the
// mark or re-typesetting the wordmark. Sans sits at 600 because main.js bundles
// IBM Plex Sans 400/500/600 only; asking for 700 would synthesise a fake bold.
//
// The hall is traced from the published TTC26 floor plan into our own geometry,
// in the plan's own pixel space (wall box 35,45 -> 1388,380). Every stand on the
// floor is drawn and none is labelled: the sentence says "all these networks",
// and a hundred anonymous stands with one of them lit is that sentence as a
// picture. Naming a neighbour would also be a claim we have not earned.
import { computed } from 'vue'

// Poster canvas, and where the hall band sits in it.
const PLAN = { x: 60, y: 415, w: 1080, sx: 35, sy: 45, sw: 1353, sh: 335 }
const K = PLAN.w / PLAN.sw
const X = (v) => PLAN.x + (v - PLAN.sx) * K
const Y = (v) => PLAN.y + (v - PLAN.sy) * K
const W = (v) => v * K

// Fixed structures: stages, lounges, workshop rooms, the walls of solutions,
// registration, cloakroom. Everything here is a room, not an exhibitor.
const BLOCKS = [
  [141, 49, 329, 93], [39, 49, 98, 91], [757, 49, 60, 81], [904, 55, 120, 63],
  [1159, 55, 131, 46], [1326, 55, 51, 71], [1163, 113, 123, 23], [1163, 138, 123, 22],
  [1322, 156, 24, 23], [1322, 185, 24, 65], [108, 157, 178, 54], [546, 196, 57, 57],
  [633, 196, 57, 57], [794, 196, 57, 57], [1231, 181, 57, 57], [720, 203, 44, 43],
  [782, 331, 44, 44], [1334, 328, 43, 43], [149, 312, 175, 72], [329, 312, 175, 72],
  [516, 300, 80, 78],
]
// Theatre seating blocks.
const SEATS = [107, 171, 235].flatMap((x) => [225, 235, 254, 264].map((y) => [x, y, 52, 4]))

// Stands. Rows are runs at a fixed pitch, exactly as the floor plan sets them.
const rowOf = (x0, n, y, w, h, pitch) =>
  Array.from({ length: n }, (_, i) => [x0 + i * pitch, y, w, h])
const STANDS = [
  ...rowOf(369, 5, 151, 24, 19, 24.2),      // S11-S15
  ...rowOf(510, 12, 151, 24, 19, 24.2),     // S16-S27, ours is the last
  ...rowOf(1030, 3, 67, 24, 19, 24.2),      // S1-S3
  ...rowOf(435, 3, 276, 24, 18, 24.2),      // S35-S37
  ...rowOf(896, 16, 299, 24, 19, 24.2),     // S38-S53
  ...rowOf(903, 13, 256, 27, 27, 29.3),     // M9-M21
  [374, 197, 27, 27], [402, 197, 28, 27], [460, 197, 28, 27], [490, 197, 27, 27],
  [374, 226, 27, 27], [403, 226, 27, 27], [461, 226, 27, 27], [490, 226, 27, 27],
  [387, 267, 28, 28],                       // M22
  ...[912, 952, 1018, 1058, 1123, 1163].flatMap((x) => [[x, 178, 38, 27], [x, 207, 38, 27]]),
]
const US = [776, 151, 24, 19]               // S27

const box = (a) => ({ x: X(a[0]), y: Y(a[1]), width: W(a[2]), height: W(a[3]) })
const blocks = computed(() => [...BLOCKS, ...SEATS].map(box))
const stands = computed(() => STANDS.map(box))
const us = computed(() => box(US))
const hall = computed(() => ({ x: X(35), y: Y(45), width: W(1353), height: W(335) }))
// Centre of our stand: the beacon, the label and the foot of the leader line.
const ux = computed(() => X(US[0]) + W(US[2]) / 2)
const uy = computed(() => Y(US[1]) + W(US[3]) / 2)
// The main entrance, on the right-hand wall.
const ex = computed(() => X(1388))
const ey = computed(() => Y(203))
</script>

<template>
  <svg class="poster" viewBox="0 0 1280 720" preserveAspectRatio="xMidYMid meet" aria-hidden="true">
    <!-- approved lockup, placed as artwork and never re-drawn -->
    <image href="/meshsat-lockup.png" x="60" y="56" width="300" height="61" />

    <text x="60" y="196" class="lead">MeshSat's job is simple:</text>
    <text x="60" y="278" class="say">make all these networks</text>
    <text x="60" y="352" class="say">at TTC26 talk to each other.</text>

    <!-- the hall: rooms recede, stands are the texture, one of them is ours -->
    <rect v-bind="hall" class="wall" rx="3" />
    <rect v-for="(b, i) in blocks" :key="'b' + i" v-bind="b" class="block" rx="1.2" />
    <rect v-for="(s, i) in stands" :key="'s' + i" v-bind="s" class="stand" rx="1.2" />
    <rect v-bind="us" class="us" rx="1.2" />

    <!-- from the sentence down to the stand it is talking about -->
    <line :x1="ux" :y1="uy - 16" :x2="ux" y2="378" class="leader" />
    <circle class="beacon" :cx="ux" :cy="uy" />
    <text :x="ux + 46" :y="uy + 10" class="uslabel">S27</text>

    <line :x1="ex" :y1="ey - 15" :x2="ex" :y2="ey + 15" class="doortick" />
    <text :x="ex + 14" :y="ey + 7" class="way">entrance</text>
  </svg>
</template>

<style scoped>
.poster { width: 100%; height: 100%; display: block; background: #040406; }
.lead {
  font-family: 'IBM Plex Mono', ui-monospace, monospace;
  font-size: 26px; font-weight: 400; fill: #C8B89A; letter-spacing: 0.02em;
}
.say {
  font-family: 'IBM Plex Sans', system-ui, sans-serif;
  font-size: 66px; font-weight: 600; fill: #F7F7F4; letter-spacing: -0.022em;
}
.uslabel {
  font-family: 'IBM Plex Mono', ui-monospace, monospace;
  font-size: 28px; font-weight: 500; fill: #F96118; letter-spacing: 0.06em;
}
.way {
  font-family: 'IBM Plex Sans', system-ui, sans-serif;
  font-size: 22px; font-weight: 400; fill: #C8B89A; opacity: 0.55;
}
.wall { fill: #08080C; stroke: #2B2B35; stroke-width: 1.5; }
.block { fill: #101016; }
.stand { fill: #D6C7A9; opacity: 0.5; }
.us { fill: #F96118; }
.leader { stroke: #F96118; stroke-width: 1.5; opacity: 0.7; }
.doortick { stroke: #C8B89A; stroke-width: 3; opacity: 0.5; }
/* One motion on the poster, and it is the thing a radio stand does. */
.beacon {
  fill: none; stroke: #F96118; stroke-width: 3;
  animation: ping 6s cubic-bezier(0.16, 0.7, 0.3, 1) infinite;
}
@keyframes ping {
  0% { r: 8px; opacity: 0.9; }
  55% { r: 34px; opacity: 0; }
  100% { r: 34px; opacity: 0; }
}
@media (prefers-reduced-motion: reduce) {
  .beacon { animation: none; r: 22px; opacity: 0.45; }
}
</style>
