<script setup>
// LilyGO T-Deck Plus, front view: 2.8 inch screen over a 35-key keyboard,
// trackball at the lower left, SMA stub top left. Drawn in the TTC mode
// line language (Sand strokes on near-black). [MESHSAT-826]
defineProps({ scale: { type: Number, default: 1.35 } })
</script>

<template>
  <g class="device" :transform="`scale(${scale})`">
    <rect x="-60" y="-38" width="120" height="76" rx="9" class="body" />
    <rect x="-54" y="-32" width="108" height="42" rx="2" class="bezel" />
    <rect x="-50" y="-29" width="100" height="36" rx="1" class="screen" />
    <g class="ui">
      <rect x="-46" y="-25" width="44" height="7" rx="3" class="bubble" />
      <rect x="-2" y="-15" width="48" height="7" rx="3" class="bubble far" />
      <rect x="-46" y="-5" width="30" height="7" rx="3" class="bubble" />
    </g>
    <circle cx="-49" cy="18" r="5.5" class="trackball" /><circle cx="-49" cy="18" r="2.4" class="trackball-in" />
    <g class="keys">
      <rect v-for="k in 10" :key="'r1'+k" :x="-39 + (k-1)*9" y="13" width="7.6" height="4.6" rx="1" />
      <rect v-for="k in 10" :key="'r2'+k" :x="-39 + (k-1)*9" y="19" width="7.6" height="4.6" rx="1" />
      <rect v-for="k in 10" :key="'r3'+k" :x="-39 + (k-1)*9" y="25" width="7.6" height="4.6" rx="1" />
      <rect x="-39" y="31" width="16.6" height="4.6" rx="1" /><rect x="-21" y="31" width="43.6" height="4.6" rx="1" /><rect x="24" y="31" width="25.6" height="4.6" rx="1" />
    </g>
    <rect x="-58" y="-48" width="7" height="10" rx="1.5" class="sma" />
    <line x1="-54.5" y1="-48" x2="-60" y2="-72" class="ant" />
  </g>
</template>
