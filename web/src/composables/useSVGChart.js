/**
 * useSVGChart.js
 *
 * Lightweight SVG sparkline composable — generates polyline points and area
 * paths for inline SVG charts without any chart library dependency.
 * Ported from CubeOS dashboard (UptimeLoadWidget pattern).
 */

/**
 * Build SVG polyline points string from data array.
 * @param {Array} data - Array of data points
 * @param {Function} accessor - Function to extract numeric value from each point
 * @param {number} width - SVG viewBox width
 * @param {number} height - SVG viewBox height
 * @param {number} [yMin=0] - Minimum Y value for scaling
 * @param {number} [yMax=100] - Maximum Y value for scaling
 * @returns {string} SVG polyline points attribute string
 */
export function buildPolyline(data, accessor, width, height, yMin = 0, yMax = 100) {
  if (!data || data.length < 2) return ''

  const padding = 2
  const w = width - padding * 2
  const h = height - padding * 2
  const range = yMax - yMin || 1

  const points = data.map((point, i) => {
    const x = padding + (i / (data.length - 1)) * w
    const raw = accessor(point) ?? yMin
    const val = Math.max(yMin, Math.min(yMax, raw))
    const y = padding + h - ((val - yMin) / range) * h
    return `${x.toFixed(1)},${y.toFixed(1)}`
  })

  return points.join(' ')
}

/**
 * Build SVG area fill path (closed polygon below the line).
 * @param {Array} data - Array of data points
 * @param {Function} accessor - Function to extract numeric value from each point
 * @param {number} width - SVG viewBox width
 * @param {number} height - SVG viewBox height
 * @param {number} [yMin=0] - Minimum Y value for scaling
 * @param {number} [yMax=100] - Maximum Y value for scaling
 * @returns {string} SVG path d attribute string
 */
export function buildAreaPath(data, accessor, width, height, yMin = 0, yMax = 100) {
  if (!data || data.length < 2) return ''

  const padding = 2
  const w = width - padding * 2
  const h = height - padding * 2
  const range = yMax - yMin || 1

  let path = `M ${padding},${padding + h}`
  data.forEach((point, i) => {
    const x = padding + (i / (data.length - 1)) * w
    const raw = accessor(point) ?? yMin
    const val = Math.max(yMin, Math.min(yMax, raw))
    const y = padding + h - ((val - yMin) / range) * h
    path += ` L ${x.toFixed(1)},${y.toFixed(1)}`
  })
  path += ` L ${padding + w},${padding + h} Z`

  return path
}

/**
 * Get data index from mouse hover position over chart element.
 * @param {MouseEvent} event - Mouse event from the chart container
 * @param {number} dataLength - Length of the data array
 * @param {number} [width] - Optional explicit width (uses element width if omitted)
 * @returns {number} Clamped index into the data array
 */
export function getHoverIndex(event, dataLength, width) {
  if (dataLength < 1) return 0
  const rect = event.currentTarget.getBoundingClientRect()
  const x = event.clientX - rect.left
  const w = width || rect.width
  const ratio = x / w
  const idx = Math.round(ratio * (dataLength - 1))
  return Math.max(0, Math.min(idx, dataLength - 1))
}
