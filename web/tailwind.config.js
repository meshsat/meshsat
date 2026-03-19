/** @type {import('tailwindcss').Config} */
export default {
  content: [
    './index.html',
    './src/**/*.{vue,js}'
  ],
  theme: {
    extend: {
      fontFamily: {
        mono: ['JetBrains Mono', 'ui-monospace', 'SFMono-Regular', 'monospace'],
        display: ['Oxanium', 'system-ui', 'sans-serif']
      },
      colors: {
        // MeshSat brand palette (matches meshsat-android Color.kt + meshsat-hub)
        brand: {
          primary: '#0D9488',   // teal-600
          accent: '#14B8A6',    // teal-500
          dark: '#111827',      // gray-900
          surface: '#1F2937',   // gray-800
          text: '#E5E7EB',      // gray-200
        },
        // Transport badge colors (consistent across Bridge, Hub, Android)
        transport: {
          mesh: '#06B6D4',      // cyan-500
          iridium: '#A855F7',   // purple-500
          cellular: '#F97316',  // orange-500
          sms: '#22C55E',       // green-500
        },
        mesh: {
          50: '#f0fdfa',
          100: '#ccfbf1',
          200: '#99f6e4',
          300: '#5eead4',
          400: '#2dd4bf',
          500: '#14b8a6',
          600: '#0d9488',
          700: '#0f766e',
          800: '#115e59',
          900: '#134e4a',
          950: '#042f2e'
        },
        tactical: {
          bg: '#111827',
          surface: '#1F2937',
          border: '#374151',
          iridium: '#A855F7',
          lora: '#06B6D4',
          gps: '#818cf8',
          sos: '#ef4444',
          power: '#10b981'
        }
      }
    }
  },
  plugins: []
}
