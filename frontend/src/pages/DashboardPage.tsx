import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useEffect, useRef } from 'react'
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
  const runs = useQuery({
    queryKey: ['sync-runs'],
    queryFn: () => apiGet<SyncRunsResponse>('/api/sync-runs'),
    // Poll while a sync runs, so its result shows up without a reload.
    refetchInterval: (query) => (query.state.data?.runs.some((run) => run.status === 'running') ? 2000 : false),
  })
  const trigger = useMutation({
    mutationFn: (kind: SyncRun['kind']) => apiSend('POST', `/api/sync-runs/${kind}`),
    onSettled: () => queryClient.invalidateQueries({ queryKey: ['sync-runs'] }),
  })
  // When a run finishes, the data it synced is new: refresh everything else.
  const running = runs.data?.runs.some((run) => run.status === 'running') ?? false
  const wasRunning = useRef(false)
  useEffect(() => {
    if (wasRunning.current && !running) {
      void queryClient.invalidateQueries({ predicate: (query) => query.queryKey[0] !== 'sync-runs' })
    }
    wasRunning.current = running
  }, [running, queryClient])
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
              // Runs are newest first per kind.
              const last = runs.data?.runs.find((run) => run.kind === kind)
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
                        disabled={last?.status === 'running' || (trigger.isPending && trigger.variables === kind)}
                        onClick={() => trigger.mutate(kind)}
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
