<script setup>
// LilyGO T-Echo, front view: the product photo (LilyGO GitHub, Meshtastic
// edition, annotations and white background removed) with the e-paper
// covered by a live message list in the TTC line language. Geometry in
// device units: the photo is 450 x 1052 px scaled to 50 x 116.9, e-paper
// at photo (122..352, 730..962). The invisible .body rect carries the
// flash outline. [MESHSAT-826]
defineProps({ scale: { type: Number, default: 1.35 } })
</script>

<template>
  <g class="device photo paper techo" :transform="`scale(${scale})`">
    <image href="/techo.png" x="-25" y="-70" width="50" height="116.9" class="photo-img" preserveAspectRatio="xMidYMid meet" />
    <rect x="-25.6" y="1.6" width="51.2" height="45.9" rx="5" class="body" />
    <rect x="-11.4" y="11.1" width="25.6" height="25.8" class="epaper" />
    <g class="ink">
      <rect x="-9.4" y="13.6" width="14" height="1.9" rx="0.6" /><rect x="-9.4" y="17.1" width="20" height="1.9" rx="0.6" />
      <rect x="-9.4" y="20.6" width="16" height="1.9" rx="0.6" /><rect x="-9.4" y="24.1" width="21" height="1.9" rx="0.6" />
      <rect x="-9.4" y="30.6" width="12" height="1.9" rx="0.6" />
    </g>
  </g>
</template>
