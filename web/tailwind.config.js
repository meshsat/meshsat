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
          bg: '#0a0e14',
          surface: '#111820',
          border: '#1a2230',
          iridium: '#f59e0b',
          lora: '#00d4aa',
          gps: '#818cf8',
          sos: '#ef4444',
          power: '#10b981'
        }
      }
    }
  },
  plugins: []
}
