<script setup>
// LilyGO T-Deck Pro, front view: the product photo (LilyGO store, white
// background removed) with the demo firmware's e-paper covered by a live
// message list in the TTC line language. Geometry in device units: the
// photo is 369 x 665 px scaled to 66.6 x 120, e-paper at photo
// (55..315, 42..390). The invisible .body rect carries the flash
// outline. [MESHSAT-826]
defineProps({ scale: { type: Number, default: 1.35 } })
</script>

<template>
  <g class="device photo paper tdeckpro" :transform="`scale(${scale})`">
    <image href="/tdeck-pro.png" x="-33.3" y="-79" width="66.6" height="120" class="photo-img" preserveAspectRatio="xMidYMid meet" />
    <rect x="-33.6" y="-79.4" width="67.2" height="120.8" rx="7" class="body" />
    <rect x="-23.4" y="-71.4" width="46.9" height="62.8" rx="0.8" class="epaper" />
    <g class="ink">
      <rect x="-19.4" y="-65.4" width="22" height="2.4" rx="0.8" /><rect x="-19.4" y="-59.6" width="34" height="2.4" rx="0.8" />
      <rect x="-19.4" y="-53.8" width="28" height="2.4" rx="0.8" /><rect x="-19.4" y="-48" width="36" height="2.4" rx="0.8" />
      <rect x="-19.4" y="-42.2" width="18" height="2.4" rx="0.8" /><rect x="-19.4" y="-36.4" width="31" height="2.4" rx="0.8" />
      <rect x="-19.4" y="-30.6" width="25" height="2.4" rx="0.8" /><rect x="-19.4" y="-24.8" width="14" height="2.4" rx="0.8" />
    </g>
  </g>
</template>
