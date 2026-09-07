<script setup>
// LilyGO T-Deck Plus, front view: the product photo (LilyGO store, white
// background removed) with the demo firmware's screen covered by a live
// conversation drawn in the TTC line language. Geometry in device units:
// the photo is 524 x 878 px scaled to 72 x 120.6, screen at photo
// (52..478, 176..494). The invisible .body rect carries the flash
// outline. [MESHSAT-826]
defineProps({ scale: { type: Number, default: 1.35 } })
</script>

<template>
  <g class="device photo tdeck" :transform="`scale(${scale})`">
    <image href="/tdeck-plus.png" x="-36" y="-79" width="72" height="120.6" class="photo-img" preserveAspectRatio="xMidYMid meet" />
    <rect x="-36.6" y="-71.4" width="73.2" height="113.6" rx="8" class="body" />
    <rect x="-28.9" y="-54.8" width="58.5" height="43.7" rx="0.6" class="screen" />
    <g class="ui">
      <rect x="-25.5" y="-50.5" width="27" height="5.6" rx="2.8" class="bubble" />
      <rect x="-1.5" y="-41.5" width="29" height="5.6" rx="2.8" class="bubble far" />
      <rect x="-25.5" y="-32.5" width="21" height="5.6" rx="2.8" class="bubble" />
      <rect x="2" y="-23.5" width="24" height="5.6" rx="2.8" class="bubble far" />
    </g>
  </g>
</template>
