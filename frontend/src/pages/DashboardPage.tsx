import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useEffect, useRef, useState } from 'react'
import { Link } from 'react-router-dom'
import { apiGet, apiSend, errMessage } from '../api/client'
import type { ListsResponse, Page, SyncRun, SyncRunsResponse } from '../api/types'
import { ListsTable } from '../components/ListsTable'
import { formatTime } from '../format'
import { ROLE_MANAGE_NEWSLETTERS, useHasRole } from '../roles'

const SYNC_KINDS: { kind: SyncRun['kind']; label: string }[] = [
  { kind: 'listmonk_lists', label: 'Listmonk newsletters' },
  { kind: 'listmonk_subscriptions', label: 'Listmonk subscriptions' },
  { kind: 'orca_members', label: 'Orca members' },
]

export function DashboardPage() {
  const lists = useQuery({ queryKey: ['lists'], queryFn: () => apiGet<ListsResponse>('/api/lists') })
  const canManage = useHasRole(ROLE_MANAGE_NEWSLETTERS)
  const queryClient = useQueryClient()
  // Kind → ID of its newest run when "Run now" was clicked (null: none yet).
  // The 202 can come before the new run's row exists, or after it finished,
  // so poll until a newer run of that kind has finished.
  // shortcut: polls while the page is open if the run never starts (the server logs why).
  const [awaiting, setAwaiting] = useState<Partial<Record<SyncRun['kind'], string | null>>>({})
  const runs = useQuery({
    queryKey: ['sync-runs'],
    queryFn: () => apiGet<SyncRunsResponse>('/api/sync-runs'),
    // Poll while a sync runs, so its result shows up without a reload.
    refetchInterval: (query) =>
      query.state.data?.runs.some((run) => run.status === 'running') || Object.keys(awaiting).length > 0 ? 2000 : false,
  })
  // Runs are newest first per kind.
  const newest = (kind: SyncRun['kind']) => runs.data?.runs.find((run) => run.kind === kind)
  const trigger = useMutation({
    mutationFn: ({ kind }: { kind: SyncRun['kind']; previousID: string | null }) =>
      apiSend('POST', `/api/sync-runs/${kind}`),
    onSuccess: (_, { kind, previousID }) => setAwaiting((current) => ({ ...current, [kind]: previousID })),
    onSettled: () => queryClient.invalidateQueries({ queryKey: ['sync-runs'] }),
  })
  // When a run finishes, the data it synced is new: refresh everything else.
  const running = runs.data?.runs.some((run) => run.status === 'running') ?? false
  const wasRunning = useRef(false)
  useEffect(() => {
    const done = (Object.keys(awaiting) as SyncRun['kind'][]).filter((kind) => {
      const last = runs.data?.runs.find((run) => run.kind === kind)
      return last && last.id !== awaiting[kind] && last.status !== 'running'
    })
    if ((wasRunning.current && !running) || done.length > 0) {
      void queryClient.invalidateQueries({ predicate: (query) => query.queryKey[0] !== 'sync-runs' })
    }
    if (done.length > 0) {
      setAwaiting((current) => Object.fromEntries(Object.entries(current).filter(([kind]) => !done.includes(kind as SyncRun['kind']))))
    }
    wasRunning.current = running
  }, [runs.data, awaiting, running, queryClient])
  // Only the totals are needed: one item per request.
  const pendingChanges = useQuery({
    queryKey: ['changes', 'pending-count'],
    queryFn: () => apiGet<Page>('/api/changes?status=pending&limit=1'),
  })
  const deadJobs = useQuery({
    queryKey: ['jobs', 'dead-count'],
    queryFn: () => apiGet<Page>('/api/jobs?status=dead&limit=1'),
  })

  return (
    <>
      <h2>Dashboard</h2>
      <section className="tiles">
        <Tile to="/changes" label="Pending changes" value={pendingChanges.data?.total} error={pendingChanges.error} />
        <Tile to="/jobs" label="Listmonk sync errors" value={deadJobs.data?.total} error={deadJobs.error} />
      </section>

      {trigger.error && <p className="error">{errMessage(trigger.error)}</p>}
      {runs.error ? (
        <p className="error">{errMessage(runs.error)}</p>
      ) : (
        <table>
          <thead>
            <tr>
              <th>Sync status</th>
              <th>Last run</th>
              <th>Status</th>
              <th className="number">Fetched</th>
              <th className="number">Changes</th>
              <th>Error</th>
              {canManage && <th />}
            </tr>
          </thead>
          <tbody>
            {SYNC_KINDS.map(({ kind, label }) => {
              const last = newest(kind)
              return (
                <tr key={kind}>
                  <td>{label}</td>
                  <td>{last ? formatTime(last.started_at) : runs.isLoading ? '…' : 'never'}</td>
                  <td>{last && <span className={`status status-${last.status}`}>{last.status}</span>}</td>
                  <td className="number">{last?.items_fetched}</td>
                  <td className="number">{last?.changes_detected}</td>
                  <td className="error">{last?.error}</td>
                  {canManage && (
                    <td>
                      <button
                        disabled={last?.status === 'running' || (trigger.isPending && trigger.variables.kind === kind)}
                        onClick={() => trigger.mutate({ kind, previousID: last?.id ?? null })}
                      >
                        {last?.status === 'running' ? 'Running…' : 'Run now'}
                      </button>
                    </td>
                  )}
                </tr>
              )
            })}
          </tbody>
        </table>
      )}

      {lists.error ? (
        <p className="error">{errMessage(lists.error)}</p>
      ) : (
        <ListsTable lists={lists.data?.lists ?? []} />
      )}
    </>
  )
}

function Tile({ to, label, value, error }: { to: string; label: string; value: number | undefined; error: unknown }) {
  return (
    <Link to={to} className="tile">
      <div className="tile-value">{error ? '!' : (value ?? '…')}</div>
      <div className="tile-label">{error ? errMessage(error) : label}</div>
    </Link>
  )
}
