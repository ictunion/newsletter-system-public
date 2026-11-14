// Response shapes of the newsletter-system API (see the Swagger UI at
// /api/docs/index.html when ENABLE_SWAGGER_DOCS is set).

export type Visibility = 'private' | 'members' | 'public'
export type SubscriptionStatus = 'subscribed' | 'unsubscribed' | 'removed'

export interface NewsletterList {
  id: string
  name: string
  description: string
  visibility: Visibility
  listmonk_id: number | null
  listmonk_subscriber_count: number | null
  synced_at: string | null
  deleted_at: string | null
  subscribed_count: number
  unsubscribed_count: number
  removed_count: number
}

export interface ListsResponse {
  lists: NewsletterList[]
}

export interface ListSubscriber {
  subscriber_id: string
  email: string
  name: string
  member_number: number | null
  status: SubscriptionStatus
  source: 'rule' | 'workplace' | 'manual' | 'self'
  rule_id: string | null
  listmonk_status: string | null
  created_at: string
  updated_at: string
}

export interface Page {
  total: number
  limit: number
  offset: number
}

export interface ListSubscribersResponse extends Page {
  subscribers: ListSubscriber[]
}

export interface SyncRun {
  id: string
  kind: 'listmonk_lists' | 'listmonk_subscriptions' | 'orca_members'
  status: 'running' | 'succeeded' | 'failed'
  started_at: string
  finished_at: string | null
  items_fetched: number
  changes_detected: number
  error: string | null
}

export interface SyncRunsResponse {
  runs: SyncRun[]
}

export interface Subscription {
  list_id: string
  list_name: string
  listmonk_id: number | null
  list_deleted_at: string | null
  status: SubscriptionStatus
  source: 'rule' | 'workplace' | 'manual' | 'self'
  rule_id: string | null
  listmonk_status: string | null
  created_at: string
  updated_at: string
}

export interface Subscriber {
  id: string
  email: string
  name: string
  member_number: number | null
  city: string | null
  lang: string | null
  listmonk_subscriber_id: number | null
  created_at: string
  updated_at: string
  subscriptions: Subscription[]
}

export interface Rule {
  id: string
  kind: 'all' | 'city'
  list_id: string
  city_contains: string | null
  enabled: boolean
  note: string | null
  created_at: string
}

export interface RulesResponse {
  rules: Rule[]
}

export type ChangeKind =
  | 'member_new'
  | 'member_updated'
  | 'member_removed'
  | 'subscription_add'
  | 'subscription_remove'
  | 'list_removed'

// The member data orca sends (member_new and member_updated payloads).
export interface OrcaMember {
  member_number: number
  email: string
  name: string
  city: string | null
  lang: string
  workplace_newsletter_id: number | null
}

export interface Change {
  id: string
  kind: ChangeKind
  member_number: number | null
  list_id: string | null
  list_name?: string
  payload: Record<string, unknown>
  status: 'pending' | 'approved' | 'rejected'
  detected_at: string
  decided_at: string | null
  decided_by: string | null
}

export interface ChangesResponse extends Page {
  changes: Change[]
}

export interface BulkApproveResponse {
  approved: Change[]
  failed: { id: string; error: string }[]
}

export interface Job {
  id: string
  kind: 'push_subscriber' | 'delete_subscriber'
  subscriber_id: string | null
  listmonk_subscriber_id: number | null
  status: 'pending' | 'dead'
  attempt_count: number
  next_attempt_at: string
  last_error: string | null
  created_at: string
  updated_at: string
  subscriber_email: string | null
  subscriber_member_number: number | null
}

export interface JobsResponse extends Page {
  jobs: Job[]
}

export interface AuditLog {
  id: string
  created_at: string
  actor_sub: string
  actor_email: string | null
  category: string
  action: string
  method: string
  route: string
  path: string
  status_code: number
}

export interface AuditLogsResponse extends Page {
  logs: AuditLog[]
}
