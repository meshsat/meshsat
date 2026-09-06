/** @type {import('tailwindcss').Config} */
const ORANGE = {
  50: '#FFF3EC', 100: '#FFE2D1', 200: '#FFC4A6', 300: '#FF9F6E', 400: '#FF7C3B',
  500: '#F96118', 600: '#BF450B', 700: '#9C3808', 800: '#7A2C07', 900: '#6E2A0D', 950: '#3D1405'
}
const SAND = {
  50: '#FAF8F3', 100: '#F1ECE1', 200: '#E4DAC6', 300: '#D6C7A9', 400: '#C8B89A',
  500: '#AE9C7A', 600: '#7A6B50', 700: '#5C5040', 800: '#433A2C', 900: '#332C21', 950: '#1B1711'
}

export default {
  content: [
    './index.html',
    './src/**/*.{vue,js}'
  ],
  theme: {
    extend: {
      // Brand type (MeshSat brand guide, MESHSAT-826): IBM Plex Mono for
      // display sizes and traces, IBM Plex Sans for body. Display is
      // monospace on purpose: the product's native artifact is a message
      // trace, and meshsat.net commits to the same pair.
      fontFamily: {
        sans: ['IBM Plex Sans', 'system-ui', 'sans-serif'],
        mono: ['IBM Plex Mono', 'ui-monospace', 'SFMono-Regular', 'monospace'],
        display: ['IBM Plex Mono', 'ui-monospace', 'monospace']
      },
      colors: {
        // MeshSat brand palette (brand guide, approved 2026-08-28; MESHSAT-826).
        // Space Black #040406, Signal Orange #F96118, Off White #F7F7F4,
        // Sand #C8B89A (from the field-kit photo). The stock grey, teal, mesh,
        // cyan, sky, blue, indigo, violet and purple scales are REDEFINED here
        // so the ~4,000 existing utility classes repaint without edits:
        //   grey  -> warm near-black scale between Space Black and Off White
        //   teal / mesh -> Signal Orange scale (the accent everywhere)
        //   cyan / sky / blue / indigo / violet / purple -> Sand scale (one
        //   quiet secondary hue for bearers, links and the far side)
        // emerald / green (delivered, up), amber (healing, warning) and red
        // (failed) keep their stock values: state colours are functional only.
        brand: {
          primary: '#F96118',
          accent: '#FF7C3B',
          dark: '#040406',
          surface: '#15151B',
          text: '#F7F7F4',
          sand: '#C8B89A',
        },
        transport: {
          mesh: '#C8B89A',
          iridium: '#E0B458',
          cellular: '#F96118',
          sms: '#22C55E',
        },
        gray: {
          50: '#F7F7F4',
          100: '#EBEBEE',
          200: '#D6D6DC',
          300: '#B4B4BD',
          400: '#8A8A96',
          500: '#5C5C68',
          600: '#3A3A44',
          700: '#24242C',
          800: '#15151B',
          900: '#0B0B0F',
          950: '#040406',
        },
        teal: ORANGE,
        mesh: ORANGE,
        cyan: SAND,
        sky: SAND,
        blue: SAND,
        indigo: SAND,
        violet: SAND,
        purple: SAND,
        tactical: {
          bg: '#040406',
          surface: '#15151B',
          border: '#24242C',
          iridium: '#E0B458',
          lora: '#C8B89A',
          gps: '#C8B89A',
          sos: '#ef4444',
          power: '#10b981'
        }
      }
    }
  },
  plugins: []
}
