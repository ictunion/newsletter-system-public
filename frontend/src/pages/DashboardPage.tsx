import { useQuery } from '@tanstack/react-query'
import { Link } from 'react-router-dom'
import { apiGet, errMessage } from '../api/client'
import type { ListsResponse, Page, SyncRun, SyncRunsResponse } from '../api/types'
import { formatTime } from '../format'

const SYNC_KINDS: { kind: SyncRun['kind']; label: string }[] = [
  { kind: 'listmonk_lists', label: 'Listmonk lists' },
  { kind: 'listmonk_subscriptions', label: 'Listmonk subscriptions' },
  { kind: 'orca_members', label: 'orca members' },
]

export function DashboardPage() {
  const lists = useQuery({ queryKey: ['lists'], queryFn: () => apiGet<ListsResponse>('/api/lists') })
  const runs = useQuery({ queryKey: ['sync-runs'], queryFn: () => apiGet<SyncRunsResponse>('/api/sync-runs') })
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
        <Tile to="/jobs" label="Dead jobs" value={deadJobs.data?.total} error={deadJobs.error} />
      </section>

      <h3>Syncs</h3>
      {runs.error ? (
        <p className="error">{errMessage(runs.error)}</p>
      ) : (
        <table>
          <thead>
            <tr>
              <th>Sync</th>
              <th>Last run</th>
              <th>Status</th>
              <th className="number">Fetched</th>
              <th className="number">Changes</th>
              <th>Error</th>
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
                </tr>
              )
            })}
          </tbody>
        </table>
      )}

      <h3>Lists</h3>
      {lists.error ? (
        <p className="error">{errMessage(lists.error)}</p>
      ) : (
        <table>
          <thead>
            <tr>
              <th>List</th>
              <th className="number">Subscribed</th>
              <th className="number">Unsubscribed</th>
              <th className="number" title="Listmonk counts every membership, unsubscribed ones included">
                In Listmonk
              </th>
            </tr>
          </thead>
          <tbody>
            {lists.data?.lists.map((list) => {
              // Listmonk's count includes unsubscribed memberships; removed rows are not in Listmonk.
              const mismatch =
                list.listmonk_subscriber_count !== null &&
                list.listmonk_subscriber_count !== list.subscribed_count + list.unsubscribed_count
              return (
                <tr key={list.id}>
                  <td>
                    <Link to={`/lists/${list.id}`}>{list.name}</Link>
                  </td>
                  <td className="number">{list.subscribed_count}</td>
                  <td className="number">{list.unsubscribed_count}</td>
                  <td className={mismatch ? 'number mismatch' : 'number'} title={mismatch ? 'Differs from our count' : undefined}>
                    {list.listmonk_subscriber_count ?? '–'}
                  </td>
                </tr>
              )
            })}
          </tbody>
        </table>
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
