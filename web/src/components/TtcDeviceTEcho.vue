<script setup>
// LilyGO T-Echo, front view: 1.54 inch e-paper, one front button, two
// side buttons, SMA stub top right. TTC mode line language. [MESHSAT-826]
defineProps({ scale: { type: Number, default: 1.35 } })
</script>

<template>
  <g class="device paper" :transform="`scale(${scale})`">
    <rect x="-30" y="-46" width="60" height="92" rx="10" class="body" />
    <rect x="-25" y="-41" width="50" height="50" rx="2" class="bezel" />
    <rect x="-22" y="-38" width="44" height="44" class="epaper" />
    <g class="ink">
      <rect x="-18" y="-33" width="24" height="3" rx="1" /><rect x="-18" y="-27" width="34" height="3" rx="1" />
      <rect x="-18" y="-21" width="28" height="3" rx="1" /><rect x="-18" y="-15" width="36" height="3" rx="1" />
      <rect x="-18" y="-4" width="20" height="3" rx="1" />
    </g>
    <circle cx="0" cy="26" r="6" class="btn" /><circle cx="0" cy="26" r="2.5" class="btn-in" />
    <rect x="29" y="-20" width="3" height="10" rx="1" class="sidebtn" /><rect x="29" y="-6" width="3" height="10" rx="1" class="sidebtn" />
    <rect x="20" y="-56" width="7" height="10" rx="1.5" class="sma" />
    <line x1="23.5" y1="-56" x2="30" y2="-80" class="ant" />
  </g>
</template>
