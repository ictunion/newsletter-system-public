import type { Page } from '../api/types'

// Previous/next paging for limit/offset API pages.
export function Pager({ page, shown, onOffset }: { page: Page; shown: number; onOffset: (offset: number) => void }) {
  const { total, limit, offset } = page
  return (
    <p className="pager">
      <button disabled={offset === 0} onClick={() => onOffset(Math.max(0, offset - limit))}>
        Previous
      </button>
      {total === 0 ? 'Nothing found' : `${offset + 1}–${offset + shown} of ${total}`}
      <button disabled={offset + limit >= total} onClick={() => onOffset(offset + limit)}>
        Next
      </button>
    </p>
  )
}
