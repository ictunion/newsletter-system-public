import { useQuery } from '@tanstack/react-query'
import { useState } from 'react'
import { apiGet, errMessage } from '../api/client'
import type { ListsResponse } from '../api/types'
import { ListsTable } from '../components/ListsTable'
import { formatTime } from '../format'

export function ListsPage() {
  const [includeDeleted, setIncludeDeleted] = useState(false)
  const lists = useQuery({
    queryKey: ['lists', { includeDeleted }],
    queryFn: () => apiGet<ListsResponse>(`/api/lists${includeDeleted ? '?include_deleted=true' : ''}`),
  })
  // One Listmonk sync updates every list at once, so the newest synced_at is
  // when the lists were last synced.
  const lastSynced = lists.data?.lists.reduce<string | null>(
    (latest, list) => (list.synced_at && (!latest || list.synced_at > latest) ? list.synced_at : latest),
    null,
  )

  return (
    <>
      <h2>
        Newsletters
        {lastSynced && <span className="muted"> – last synced at {formatTime(lastSynced)}</span>}
      </h2>
      <label>
        <input type="checkbox" checked={includeDeleted} onChange={(e) => setIncludeDeleted(e.target.checked)} /> Show
        deleted lists
      </label>
      {lists.error && <p className="error">{errMessage(lists.error)}</p>}
      <ListsTable lists={lists.data?.lists ?? []} full />
    </>
  )
}

