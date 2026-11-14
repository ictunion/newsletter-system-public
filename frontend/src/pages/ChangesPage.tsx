import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import { Link } from 'react-router-dom'
import { apiGet, apiSend, errMessage } from '../api/client'
import type { BulkApproveResponse, Change, ChangeKind, ChangesResponse, OrcaMember } from '../api/types'
import { Pager } from '../components/Pager'
import { formatTime } from '../format'
import { ROLE_MANAGE_NEWSLETTERS, useHasRole } from '../roles'

// Bulk approval during initial population covers whole pages at once.
const PAGE_SIZE = 500

const KINDS: { kind: ChangeKind; label: string }[] = [
  { kind: 'member_new', label: 'New member' },
  { kind: 'member_updated', label: 'Member data changed' },
  { kind: 'member_removed', label: 'Member left' },
  { kind: 'subscription_add', label: 'Add to list' },
  { kind: 'subscription_remove', label: 'Remove from list' },
  { kind: 'list_removed', label: 'List gone from Listmonk' },
]
const kindLabel = new Map(KINDS.map(({ kind, label }) => [kind, label]))

export function ChangesPage() {
  const canManage = useHasRole(ROLE_MANAGE_NEWSLETTERS)
  const queryClient = useQueryClient()
  const [status, setStatus] = useState<Change['status']>('pending')
  const [kind, setKind] = useState<ChangeKind | ''>('')
  const [offset, setOffset] = useState(0)
  const [selected, setSelected] = useState<Set<string>>(new Set())
  const [bulkResult, setBulkResult] = useState<BulkApproveResponse>()

  const params = new URLSearchParams({ status, limit: String(PAGE_SIZE), offset: String(offset) })
  if (kind) params.set('kind', kind)
  const changes = useQuery({
    queryKey: ['changes', status, kind, offset],
    queryFn: () => apiGet<ChangesResponse>(`/api/changes?${params}`),
  })
  // Approving changes members, lists and jobs, so refresh everything.
  const refresh = () => queryClient.invalidateQueries()

  const decide = useMutation({
    mutationFn: ({ id, action }: { id: string; action: 'approve' | 'reject' }) => apiSend<Change>('POST', `/api/changes/${id}/${action}`),
    onSuccess: refresh,
  })
  const bulkApprove = useMutation({
    mutationFn: (ids: string[]) => apiSend<BulkApproveResponse>('POST', '/api/changes/approve', { ids }),
    onSuccess: (result) => {
      setBulkResult(result)
      setSelected(new Set())
      return refresh()
    },
  })

  const pending = status === 'pending'
  const shown = changes.data?.changes ?? []
  const allSelected = shown.length > 0 && shown.every((change) => selected.has(change.id))
  const toggle = (id: string) =>
    setSelected((current) => {
      const next = new Set(current)
      if (!next.delete(id)) next.add(id)
      return next
    })
  const filter = (apply: () => void) => {
    apply()
    setOffset(0)
    setSelected(new Set())
  }

  return (
    <>
      <h2>Pending changes</h2>
      <p className="muted">
        Drift found by the daily orca reconciliation and the Listmonk list sync. Nothing changes until it is approved.
      </p>
      <p>
        <label>
          Status{' '}
          <select value={status} onChange={(e) => filter(() => setStatus(e.target.value as Change['status']))}>
            <option value="pending">Pending</option>
            <option value="approved">Approved</option>
            <option value="rejected">Rejected</option>
          </select>
        </label>{' '}
        <label>
          Kind{' '}
          <select value={kind} onChange={(e) => filter(() => setKind(e.target.value as ChangeKind | ''))}>
            <option value="">All</option>
            {KINDS.map(({ kind, label }) => (
              <option key={kind} value={kind}>
                {label}
              </option>
            ))}
          </select>
        </label>
      </p>

      {canManage && pending && (
        <p>
          <button disabled={selected.size === 0 || bulkApprove.isPending} onClick={() => bulkApprove.mutate([...selected])}>
            {bulkApprove.isPending ? 'Approving…' : `Approve selected (${selected.size})`}
          </button>
        </p>
      )}
      {bulkApprove.error && <p className="error">{errMessage(bulkApprove.error)}</p>}
      {bulkResult && (
        <div>
          <p>
            Approved {bulkResult.approved.length}
            {bulkResult.failed.length > 0 && `, ${bulkResult.failed.length} failed`}.
          </p>
          {bulkResult.failed.length > 0 && (
            <ul className="error">
              {bulkResult.failed.map((failure) => (
                <li key={failure.id}>
                  {failure.id}: {failure.error}
                </li>
              ))}
            </ul>
          )}
        </div>
      )}
      {decide.error && <p className="error">{errMessage(decide.error)}</p>}
      {changes.error && <p className="error">{errMessage(changes.error)}</p>}

      <table>
        <thead>
          <tr>
            {canManage && pending && (
              <th>
                <input
                  type="checkbox"
                  checked={allSelected}
                  onChange={() => setSelected(allSelected ? new Set() : new Set(shown.map((change) => change.id)))}
                  aria-label="Select all on this page"
                />
              </th>
            )}
            <th>Change</th>
            <th>Member</th>
            <th>Details</th>
            <th>Detected</th>
            {!pending && <th>Decided</th>}
            {canManage && pending && <th />}
          </tr>
        </thead>
        <tbody>
          {shown.map((change) => (
            <tr key={change.id}>
              {canManage && pending && (
                <td>
                  <input
                    type="checkbox"
                    checked={selected.has(change.id)}
                    onChange={() => toggle(change.id)}
                    aria-label="Select change"
                  />
                </td>
              )}
              <td>{kindLabel.get(change.kind) ?? change.kind}</td>
              <td>
                {change.member_number !== null && (
                  <Link to={`/members?number=${change.member_number}`}>{change.member_number}</Link>
                )}
              </td>
              <td>
                <ChangeDetails change={change} />
              </td>
              <td>{formatTime(change.detected_at)}</td>
              {!pending && <td>{formatTime(change.decided_at)}</td>}
              {canManage && pending && (
                <td className="actions">
                  <button disabled={decide.isPending} onClick={() => decide.mutate({ id: change.id, action: 'approve' })}>
                    Approve
                  </button>
                  <button disabled={decide.isPending} onClick={() => decide.mutate({ id: change.id, action: 'reject' })}>
                    Reject
                  </button>
                </td>
              )}
            </tr>
          ))}
        </tbody>
      </table>
      {changes.data && <Pager page={changes.data} shown={shown.length} onOffset={setOffset} />}
    </>
  )
}

function ChangeDetails({ change }: { change: Change }) {
  const payload = change.payload
  switch (change.kind) {
    case 'member_new':
    case 'member_updated': {
      const member = payload as unknown as OrcaMember
      return (
        <>
          {change.kind === 'member_updated' && <span className="muted">orca now has: </span>}
          {member.name}, {member.email}, {member.city ?? 'no city'}, {member.lang}
          {member.workplace_newsletter_id !== null && `, workplace list ${member.workplace_newsletter_id}`}
        </>
      )
    }
    case 'member_removed':
      return (
        <>
          {String(payload.name)}, {String(payload.email)} <span className="muted">(not an active member in orca)</span>
        </>
      )
    case 'subscription_add':
    case 'subscription_remove':
      return (
        <>
          {change.list_name} <span className="muted">({String(payload.source)})</span>
        </>
      )
    case 'list_removed':
      return (
        <>
          {change.list_name} <span className="muted">(Listmonk list {String(payload.listmonk_id)})</span>
        </>
      )
  }
}
