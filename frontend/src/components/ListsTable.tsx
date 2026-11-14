import { useMutation, useQueryClient } from '@tanstack/react-query'
import { Link } from 'react-router-dom'
import { apiSend, errMessage } from '../api/client'
import type { NewsletterList, Visibility } from '../api/types'
import { ROLE_MANAGE_NEWSLETTERS, useHasRole } from '../roles'

// Listmonk's admin UI as the browser reaches it; without it, IDs are not links.
const LISTMONK_URL = import.meta.env.VITE_LISTMONK_URL?.replace(/\/+$/, '')

const VISIBILITIES: { value: Visibility; label: string }[] = [
  { value: 'private', label: 'Private (admins only)' },
  { value: 'members', label: 'Members' },
  { value: 'public', label: 'Public' },
]

// ListsTable shows newsletters with our counts next to Listmonk's. full adds
// the description, visibility (editable with manage-newsletters), the removed
// count and the Listmonk ID linking to Listmonk's admin UI.
export function ListsTable({ lists, full = false }: { lists: NewsletterList[]; full?: boolean }) {
  const canManage = useHasRole(ROLE_MANAGE_NEWSLETTERS)
  const statuses = full ? (['subscribed', 'unsubscribed', 'removed'] as const) : (['subscribed', 'unsubscribed'] as const)

  return (
    <table>
      <thead>
        <tr>
          <th>Newsletter</th>
          {full && <th>Visibility</th>}
          <th className="number">Subscribed</th>
          <th className="number">Unsubscribed</th>
          {full && <th className="number">Removed</th>}
          <th className="number" title="Listmonk counts every membership, unsubscribed ones included">
            In Listmonk
          </th>
          {full && <th className="number">Listmonk ID</th>}
        </tr>
      </thead>
      <tbody>
        {lists.map((list) => {
          // Listmonk's count includes unsubscribed memberships; removed rows are not in Listmonk.
          const mismatch =
            list.listmonk_subscriber_count !== null &&
            list.listmonk_subscriber_count !== list.subscribed_count + list.unsubscribed_count
          return (
            <tr key={list.id} className={list.deleted_at ? 'deleted' : undefined}>
              <td>
                <Link to={`/lists/${list.id}`}>{list.name}</Link>
                {list.deleted_at && ' (deleted)'}
                {full && list.description && <div className="muted">{list.description}</div>}
              </td>
              {full && <td>{canManage ? <VisibilitySelect list={list} /> : list.visibility}</td>}
              {statuses.map((status) => (
                <td key={status} className="number">
                  <Link to={`/lists/${list.id}?status=${status}`}>{list[`${status}_count`]}</Link>
                </td>
              ))}
              <td className={mismatch ? 'number mismatch' : 'number'} title={mismatch ? 'Differs from our count' : undefined}>
                {list.listmonk_subscriber_count ?? '–'}
              </td>
              {full && (
                <td className="number">
                  {list.listmonk_id === null ? (
                    '–'
                  ) : LISTMONK_URL ? (
                    <a href={`${LISTMONK_URL}/admin/lists/${list.listmonk_id}`} target="_blank" rel="noreferrer">
                      {list.listmonk_id}
                    </a>
                  ) : (
                    list.listmonk_id
                  )}
                </td>
              )}
            </tr>
          )
        })}
      </tbody>
    </table>
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
