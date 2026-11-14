import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import { Link } from 'react-router-dom'
import { apiGet, apiSend, errMessage } from '../api/client'
import type { ListsResponse, NewsletterList, Visibility } from '../api/types'
import { formatTime } from '../format'
import { ROLE_MANAGE_NEWSLETTERS, useHasRole } from '../roles'

const VISIBILITIES: { value: Visibility; label: string }[] = [
  { value: 'private', label: 'Private (admins only)' },
  { value: 'members', label: 'Members' },
  { value: 'public', label: 'Public' },
]

export function ListsPage() {
  const [includeDeleted, setIncludeDeleted] = useState(false)
  const canManage = useHasRole(ROLE_MANAGE_NEWSLETTERS)
  const lists = useQuery({
    queryKey: ['lists', { includeDeleted }],
    queryFn: () => apiGet<ListsResponse>(`/api/lists${includeDeleted ? '?include_deleted=true' : ''}`),
  })

  return (
    <>
      <h2>Lists</h2>
      <label>
        <input type="checkbox" checked={includeDeleted} onChange={(e) => setIncludeDeleted(e.target.checked)} /> Show
        deleted lists
      </label>
      {lists.error && <p className="error">{errMessage(lists.error)}</p>}
      <table>
        <thead>
          <tr>
            <th>List</th>
            <th>Visibility</th>
            <th className="number">Subscribed</th>
            <th className="number">Unsubscribed</th>
            <th className="number">Removed</th>
            <th>Listmonk ID</th>
            <th>Synced</th>
          </tr>
        </thead>
        <tbody>
          {lists.data?.lists.map((list) => (
            <tr key={list.id} className={list.deleted_at ? 'deleted' : undefined}>
              <td>
                <Link to={`/lists/${list.id}`}>{list.name}</Link>
                {list.deleted_at && ' (deleted)'}
                {list.description && <div className="muted">{list.description}</div>}
              </td>
              <td>{canManage ? <VisibilitySelect list={list} /> : list.visibility}</td>
              <td className="number">{list.subscribed_count}</td>
              <td className="number">{list.unsubscribed_count}</td>
              <td className="number">{list.removed_count}</td>
              <td>{list.listmonk_id ?? '–'}</td>
              <td>{formatTime(list.synced_at)}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </>
  )
}

function VisibilitySelect({ list }: { list: NewsletterList }) {
  const queryClient = useQueryClient()
  const update = useMutation({
    mutationFn: (visibility: Visibility) => apiSend<NewsletterList>('PUT', `/api/lists/${list.id}`, { visibility }),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['lists'] }),
  })

  return (
    <>
      <select
        value={update.isPending ? update.variables : list.visibility}
        disabled={update.isPending}
        onChange={(e) => update.mutate(e.target.value as Visibility)}
        aria-label={`Visibility of ${list.name}`}
      >
        {VISIBILITIES.map(({ value, label }) => (
          <option key={value} value={value}>
            {label}
          </option>
        ))}
      </select>
      {update.error && <div className="error">{errMessage(update.error)}</div>}
    </>
  )
}
