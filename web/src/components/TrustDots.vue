<script setup>
// Trust-level indicator (0-3 filled dots). [MESHSAT-553]
//
// 0  = unverified       (no key exchanged)
// 1  = auto-accepted    (key seen on the wire, no user action)
// 2  = user-confirmed   (operator accepted the key prompt)
// 3  = verified in person (scanned the contact's QR face-to-face)
//
// The schema is in place (directory_contact_keys.trust_level, v46),
// but the bridge doesn't expose it per-contact yet — we read it off
// the contact object if present and fall back to 0.
defineProps({
  level: { type: Number, default: 0 }
})
</script>

<template>
  <span class="inline-flex items-center gap-0.5" :aria-label="'Trust level ' + level + ' of 3'" :title="'Trust ' + level + '/3'">
    <span v-for="i in 3" :key="i"
      class="w-1.5 h-1.5 rounded-full transition-colors"
      :class="i <= level
        ? (level === 3 ? 'bg-tactical-iridium' : level === 2 ? 'bg-emerald-400' : 'bg-amber-400')
        : 'bg-gray-700'" />
  </span>
</template>
