import { GENRES, genreColor } from '../../map/genres'

// GenreLegend lists the genres with their colour and point count. Clicking a
// genre shows or hides it; "Only" isolates it, so any genre can be inspected
// on its own even where two colours are hard to tell apart. Hovering or
// focusing a genre dims the others on the map.
//
// Props: counts[g], hidden (Set), dark, onToggle(g), onOnly(g), onShowAll(),
// onFocusGenre(g | null).
export default function GenreLegend({ counts, hidden, dark, onToggle, onOnly, onShowAll, onFocusGenre }) {
  return (
    <div role="group" aria-label="Genres" className="flex flex-wrap items-center gap-1.5">
      {GENRES.map((g, i) => {
        const visible = !hidden.has(i)
        return (
          <span
            key={g.name}
            className="inline-flex items-center overflow-hidden rounded-full border border-slate-200 bg-white text-xs dark:border-slate-700 dark:bg-slate-800"
            onMouseEnter={() => onFocusGenre(i)}
            onMouseLeave={() => onFocusGenre(null)}
          >
            <button
              type="button"
              aria-pressed={visible}
              aria-label={`${g.name}, ${counts[i] ?? 0} tracks, ${visible ? 'shown' : 'hidden'}`}
              onClick={() => onToggle(i)}
              onFocus={() => onFocusGenre(i)}
              onBlur={() => onFocusGenre(null)}
              className={`inline-flex items-center gap-1.5 py-1 pl-2.5 pr-2 hover:bg-slate-100 dark:hover:bg-slate-700 ${
                visible ? 'text-slate-800 dark:text-slate-100' : 'text-slate-400 line-through dark:text-slate-500'
              }`}
            >
              <span
                aria-hidden="true"
                className="h-2.5 w-2.5 rounded-full"
                style={{ background: visible ? genreColor(i, dark) : 'transparent', boxShadow: `inset 0 0 0 1.5px ${genreColor(i, dark)}` }}
              />
              {g.name}
            </button>
            <button
              type="button"
              onClick={() => onOnly(i)}
              aria-label={`Show only ${g.name}`}
              className="border-l border-slate-200 px-2 py-1 text-slate-500 hover:bg-slate-100 hover:text-slate-800 dark:border-slate-700 dark:text-slate-400 dark:hover:bg-slate-700 dark:hover:text-white"
            >
              only
            </button>
          </span>
        )
      })}
      {hidden.size > 0 && (
        <button
          type="button"
          onClick={onShowAll}
          className="rounded-full px-2.5 py-1 text-xs font-medium text-indigo-600 hover:bg-indigo-50 dark:text-indigo-300 dark:hover:bg-slate-800"
        >
          Show all
        </button>
      )}
    </div>
  )
}
