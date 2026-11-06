// Genre colours. The hues and their order are the validated reference
// categorical palette of the dataviz guidelines, with separate steps for the
// light (Tailwind slate-50) and dark (slate-900) backgrounds. Adjacent legend
// entries pass the colour-vision checks on both; with eight genres in a
// scatter some non-adjacent pairs are close (e.g. orange/red), so the map
// never relies on colour alone: the legend can isolate a genre, and the
// tooltip and the side panel name it in text.

export const GENRES = [
  { name: 'Electronic', light: '#2a78d6', dark: '#3987e5' }, // blue
  { name: 'Rock', light: '#eb6834', dark: '#d95926' }, // orange
  { name: 'Folk', light: '#1baf7a', dark: '#199e70' }, // aqua
  { name: 'Instrumental', light: '#eda100', dark: '#c98500' }, // yellow
  { name: 'Pop', light: '#e87ba4', dark: '#d55181' }, // magenta
  { name: 'International', light: '#008300', dark: '#008300' }, // green
  { name: 'Experimental', light: '#6250d6', dark: '#9085e9' }, // violet
  { name: 'Hip-Hop', light: '#e34948', dark: '#e66767' }, // red
]

// Any genre not in the list (should not happen with FMA small) is drawn grey.
export const UNKNOWN_GENRE = { name: 'Other', light: '#64748b', dark: '#94a3b8' }

export function genreIndex(name) {
  const i = GENRES.findIndex((g) => g.name === name)
  return i === -1 ? GENRES.length : i
}

export function genreColor(index, dark) {
  const g = GENRES[index] ?? UNKNOWN_GENRE
  return dark ? g.dark : g.light
}
