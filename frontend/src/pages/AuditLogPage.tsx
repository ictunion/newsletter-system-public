import { useQuery } from '@tanstack/react-query'
import { useState } from 'react'
import { apiGet, errMessage } from '../api/client'
import type { AuditLogsResponse } from '../api/types'
import { Pager } from '../components/Pager'
import { formatTime } from '../format'

const PAGE_SIZE = 100

// handler.AuditCategories in internal/handler/audit.go.
const CATEGORIES = ['members', 'lists', 'rules', 'changes', 'jobs', 'sync', 'other']

export function AuditLogPage() {
  const [category, setCategory] = useState('')
  const [actor, setActor] = useState<{ sub: string; email: string | null }>()
  const [from, setFrom] = useState('')
  const [to, setTo] = useState('')
  const [offset, setOffset] = useState(0)

  const params = new URLSearchParams({ limit: String(PAGE_SIZE), offset: String(offset) })
  if (category) params.set('category', category)
  if (actor) params.set('actor_sub', actor.sub)
  if (from) params.set('from', from)
  if (to) params.set('to', to)
  const logs = useQuery({
    queryKey: ['audit-logs', category, actor?.sub, from, to, offset],
    queryFn: () => apiGet<AuditLogsResponse>(`/api/audit-logs?${params}`),
  })
  // Any filter change starts again at the first page.
  const filter = (apply: () => void) => {
    apply()
    setOffset(0)
  }

  return (
    <>
      <h2>Audit log</h2>
      <p className="muted">
        Every change made through this API: who, what and when. Rejected attempts are included, with their status code.
      </p>
      <p className="filters">
        <label>
          Category{' '}
          <select value={category} onChange={(e) => filter(() => setCategory(e.target.value))}>
            <option value="">All</option>
            {CATEGORIES.map((c) => (
              <option key={c} value={c}>
                {c}
              </option>
            ))}
          </select>
        </label>
        <label>
          From <input type="date" value={from} onChange={(e) => filter(() => setFrom(e.target.value))} />
        </label>
        <label>
          To <input type="date" value={to} onChange={(e) => filter(() => setTo(e.target.value))} />
        </label>
        {actor && (
          <span>
            By {actor.email ?? actor.sub}{' '}
            <button onClick={() => filter(() => setActor(undefined))}>Show everyone</button>
          </span>
        )}
      </p>
      {logs.error && <p className="error">{errMessage(logs.error)}</p>}
      <table>
        <thead>
          <tr>
            <th>When</th>
            <th>Who</th>
            <th>Action</th>
            <th>Request</th>
            <th className="number">Status</th>
          </tr>
        </thead>
        <tbody>
          {logs.data?.logs.map((log) => (
            <tr key={log.id}>
              <td>{formatTime(log.created_at)}</td>
              <td>
                <button
                  className="link"
                  title="Show only this person"
                  onClick={() => filter(() => setActor({ sub: log.actor_sub, email: log.actor_email }))}
                >
                  {log.actor_email ?? log.actor_sub}
                </button>
              </td>
              <td>
                {log.action} <span className="muted">({log.category})</span>
              </td>
              <td>
                <code>
                  {log.method} {log.path}
                </code>
              </td>
              <td className={log.status_code >= 400 ? 'number error' : 'number'}>{log.status_code}</td>
            </tr>
          ))}
        </tbody>
      </table>
      {logs.data && <Pager page={logs.data} shown={logs.data.logs.length} onOffset={setOffset} />}
    </>
  )
}
