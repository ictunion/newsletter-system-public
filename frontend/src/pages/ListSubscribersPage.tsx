import { useQuery } from '@tanstack/react-query'
import { useState } from 'react'
import { Link, useParams } from 'react-router-dom'
import { apiGet, errMessage } from '../api/client'
import type { ListsResponse, ListSubscribersResponse, SubscriptionStatus } from '../api/types'
import { Pager } from '../components/Pager'

const PAGE_SIZE = 100

export function ListSubscribersPage() {
  const { id } = useParams<{ id: string }>()
  const [status, setStatus] = useState<SubscriptionStatus | ''>('subscribed')
  const [offset, setOffset] = useState(0)

  // The list's name comes from the (usually cached) lists query.
  const lists = useQuery({
    queryKey: ['lists', { includeDeleted: true }],
    queryFn: () => apiGet<ListsResponse>('/api/lists?include_deleted=true'),
  })
  const list = lists.data?.lists.find((l) => l.id === id)

  const params = new URLSearchParams({ limit: String(PAGE_SIZE), offset: String(offset) })
  if (status) params.set('status', status)
  const page = useQuery({
    queryKey: ['list-subscribers', id, status, offset],
    queryFn: () => apiGet<ListSubscribersResponse>(`/api/lists/${id}/subscribers?${params}`),
  })

  return (
    <>
      <p>
        <Link to="/lists">← Lists</Link>
      </p>
      <h2>{list?.name ?? 'List'}</h2>
      <label>
        Status{' '}
        <select
          value={status}
          onChange={(e) => {
            setStatus(e.target.value as SubscriptionStatus | '')
            setOffset(0)
          }}
        >
          <option value="subscribed">Subscribed</option>
          <option value="unsubscribed">Unsubscribed (opted out)</option>
          <option value="removed">Removed</option>
          <option value="">All</option>
        </select>
      </label>
      {page.error && <p className="error">{errMessage(page.error)}</p>}
      <table>
        <thead>
          <tr>
            <th>Name</th>
            <th>Email</th>
            <th>Member</th>
            <th>Status</th>
            <th>Source</th>
            <th>Listmonk status</th>
          </tr>
        </thead>
        <tbody>
          {page.data?.subscribers.map((subscriber) => (
            <tr key={subscriber.subscriber_id}>
              <td>{subscriber.name}</td>
              <td>{subscriber.email}</td>
              <td>{subscriber.member_number ?? 'non-member'}</td>
              <td>{subscriber.status}</td>
              <td>{subscriber.source}</td>
              <td>{subscriber.listmonk_status ?? '–'}</td>
            </tr>
          ))}
        </tbody>
      </table>
      {page.data && <Pager page={page.data} shown={page.data.subscribers.length} onOffset={setOffset} />}
    </>
  )
}
