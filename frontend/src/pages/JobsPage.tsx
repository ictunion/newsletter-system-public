import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import { Link } from 'react-router-dom'
import { apiGet, apiSend, errMessage } from '../api/client'
import type { Job, JobsResponse } from '../api/types'
import { Pager } from '../components/Pager'
import { formatTime } from '../format'
import { ROLE_MANAGE_NEWSLETTERS, useHasRole } from '../roles'

const PAGE_SIZE = 100

export function JobsPage() {
  const canManage = useHasRole(ROLE_MANAGE_NEWSLETTERS)
  const queryClient = useQueryClient()
  const [status, setStatus] = useState<Job['status']>('dead')
  const [offset, setOffset] = useState(0)

  const jobs = useQuery({
    queryKey: ['jobs', status, offset],
    queryFn: () => apiGet<JobsResponse>(`/api/jobs?status=${status}&limit=${PAGE_SIZE}&offset=${offset}`),
  })
  const retry = useMutation({
    mutationFn: (id: string) => apiSend<Job>('POST', `/api/jobs/${id}/retry`),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['jobs'] }),
  })

  return (
    <>
      <h2>Listmonk sync errors</h2>
      <p className="muted">
        Changes waiting to reach Listmonk. A job that failed 10 times is dead and waits for a retry.
      </p>
      <label>
        Status{' '}
        <select
          value={status}
          onChange={(e) => {
            setStatus(e.target.value as Job['status'])
            setOffset(0)
          }}
        >
          <option value="dead">Dead</option>
          <option value="pending">Pending</option>
        </select>
      </label>
      {jobs.error && <p className="error">{errMessage(jobs.error)}</p>}
      {retry.error && <p className="error">{errMessage(retry.error)}</p>}
      <table>
        <thead>
          <tr>
            <th>Job</th>
            <th>Subscriber</th>
            <th className="number">Attempts</th>
            <th>Last error</th>
            <th>{status === 'dead' ? 'Created' : 'Next attempt'}</th>
            {canManage && status === 'dead' && <th />}
          </tr>
        </thead>
        <tbody>
          {jobs.data?.jobs.map((job) => (
            <tr key={job.id}>
              <td>{job.kind === 'push_subscriber' ? 'Push to Listmonk' : 'Delete in Listmonk'}</td>
              <td>
                {job.subscriber_member_number !== null ? (
                  <Link to={`/members?number=${job.subscriber_member_number}`}>{job.subscriber_email}</Link>
                ) : (
                  (job.subscriber_email ?? `Listmonk subscriber ${job.listmonk_subscriber_id}`)
                )}
              </td>
              <td className="number">{job.attempt_count}</td>
              <td className="error">{job.last_error}</td>
              <td>{formatTime(status === 'dead' ? job.created_at : job.next_attempt_at)}</td>
              {canManage && status === 'dead' && (
                <td>
                  <button disabled={retry.isPending} onClick={() => retry.mutate(job.id)}>
                    Retry
                  </button>
                </td>
              )}
            </tr>
          ))}
        </tbody>
      </table>
      {jobs.data && <Pager page={jobs.data} shown={jobs.data.jobs.length} onOffset={setOffset} />}
    </>
  )
}
