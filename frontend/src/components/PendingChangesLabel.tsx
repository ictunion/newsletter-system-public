import { useQuery } from '@tanstack/react-query'
import { apiGet } from '../api/client'
import type { Page } from '../api/types'

// Nav label with the number of changes waiting for approval. Shares the
// dashboard's count query, so approving refreshes both.
export function PendingChangesLabel() {
  const pending = useQuery({
    queryKey: ['changes', 'pending-count'],
    queryFn: () => apiGet<Page>('/api/changes?status=pending&limit=1'),
  })
  const total = pending.data?.total ?? 0
  return <>Changes for approval{total > 0 && ` (${total})`}</>
}
