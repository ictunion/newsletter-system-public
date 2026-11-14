import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import { Link, useLocation, useNavigate, useSearchParams } from 'react-router-dom'
import { ApiError, apiGet, apiSend, errMessage } from '../api/client'
import type { ListsResponse, Rule, RulesResponse, Subscriber, Subscription } from '../api/types'
import { formatTime } from '../format'
import { ROLE_MANAGE_NEWSLETTERS, useHasRole } from '../roles'

export function MemberPage() {
  // Opened from the header's member search, or a member link elsewhere.
  const [searchParams] = useSearchParams()
  const number = searchParams.get('number') ?? ''
  const valid = /^\d+$/.test(number)

  const member = useQuery({
    queryKey: ['member', number],
    queryFn: () => apiGet<Subscriber>(`/api/members/${number}`),
    enabled: valid,
    retry: false,
  })

  return (
    <>
      <BackLink />
      {!valid && <p>Enter a member number in the search box at the top.</p>}
      {member.isLoading && <p>Loading…</p>}
      {member.error &&
        (member.error instanceof ApiError && member.error.status === 404 ? (
          <p>No member {number} in newsletter-system.</p>
        ) : (
          <p className="error">{errMessage(member.error)}</p>
        ))}
      {member.data && <MemberDetail member={member.data} />}
    </>
  )
}

// Back to wherever the member was opened from; the dashboard when the page
// was opened directly (no earlier page in this tab).
function BackLink() {
  const navigate = useNavigate()
  const location = useLocation()
  return (
    <p>
      {location.key === 'default' ? (
        <Link to="/">← Dashboard</Link>
      ) : (
        <button className="link" onClick={() => navigate(-1)}>
          ← Back
        </button>
      )}
    </p>
  )
}

function MemberDetail({ member }: { member: Subscriber }) {
  const canManage = useHasRole(ROLE_MANAGE_NEWSLETTERS)
  const rules = useQuery({ queryKey: ['rules'], queryFn: () => apiGet<RulesResponse>('/api/rules') })
  const ruleByID = new Map(rules.data?.rules.map((rule) => [rule.id, rule]))

  return (
    <>
      <h3>
        {member.name} <span className="muted">#{member.member_number}</span>
      </h3>
      <table className="fields">
        <tbody>
          <tr>
            <th>Email</th>
            <td>{member.email}</td>
          </tr>
          <tr>
            <th>City</th>
            <td>{member.city ?? '–'}</td>
          </tr>
          <tr>
            <th>Language</th>
            <td>{member.lang}</td>
          </tr>
          <tr>
            <th>Listmonk subscriber</th>
            <td>{member.listmonk_subscriber_id ?? 'not pushed yet'}</td>
          </tr>
          <tr>
            <th>Updated</th>
            <td>{formatTime(member.updated_at)}</td>
          </tr>
        </tbody>
      </table>

      <h3>Lists</h3>
      <table>
        <thead>
          <tr>
            <th>List</th>
            <th>Status</th>
            <th>Source</th>
            <th>Listmonk status</th>
            <th>Since</th>
            {canManage && <th />}
          </tr>
        </thead>
        <tbody>
          {member.subscriptions.length === 0 && (
            <tr>
              <td colSpan={6}>Not on any list.</td>
            </tr>
          )}
          {member.subscriptions.map((subscription) => (
            <tr key={subscription.list_id} className={subscription.list_deleted_at ? 'deleted' : undefined}>
              <td>
                {subscription.list_name}
                {subscription.list_deleted_at && ' (deleted)'}
              </td>
              <td>{subscription.status === 'unsubscribed' ? 'unsubscribed (opted out)' : subscription.status}</td>
              <td>{sourceLabel(subscription, ruleByID)}</td>
              <td>{subscription.listmonk_status ?? '–'}</td>
              <td>{formatTime(subscription.updated_at)}</td>
              {canManage && (
                <td>
                  {subscription.status === 'subscribed' && (
                    <RemoveButton memberNumber={member.member_number!} subscription={subscription} />
                  )}
                </td>
              )}
            </tr>
          ))}
        </tbody>
      </table>

      {canManage && (
        <>
          <AddToList member={member} />
          <DeleteMember member={member} />
        </>
      )}
    </>
  )
}

function sourceLabel(subscription: Subscription, ruleByID: Map<string, Rule>): string {
  if (subscription.source !== 'rule') return subscription.source
  const rule = subscription.rule_id ? ruleByID.get(subscription.rule_id) : undefined
  if (!rule) return subscription.rule_id ? 'rule' : 'rule (deleted)'
  return rule.kind === 'all' ? 'rule: all members' : `rule: city contains "${rule.city_contains}"`
}

// useMemberMutation runs a change and refreshes the member and list counts.
function useMemberMutation(memberNumber: number, mutationFn: () => Promise<unknown>) {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn,
    onSuccess: () =>
      Promise.all([
        queryClient.invalidateQueries({ queryKey: ['member', String(memberNumber)] }),
        queryClient.invalidateQueries({ queryKey: ['lists'] }),
      ]),
  })
}

function RemoveButton({ memberNumber, subscription }: { memberNumber: number; subscription: Subscription }) {
  const remove = useMemberMutation(memberNumber, () =>
    apiSend('DELETE', `/api/lists/${subscription.list_id}/members/${memberNumber}`),
  )
  return (
    <>
      <button disabled={remove.isPending} onClick={() => remove.mutate()}>
        Remove
      </button>
      {remove.error && <div className="error">{errMessage(remove.error)}</div>}
    </>
  )
}

function AddToList({ member }: { member: Subscriber }) {
  const memberNumber = member.member_number!
  const [listID, setListID] = useState('')
  const lists = useQuery({ queryKey: ['lists', { includeDeleted: false }], queryFn: () => apiGet<ListsResponse>('/api/lists') })
  const subscribed = new Set(member.subscriptions.filter((s) => s.status === 'subscribed').map((s) => s.list_id))
  const add = useMemberMutation(memberNumber, () => apiSend('PUT', `/api/lists/${listID}/members/${memberNumber}`))

  return (
    <form
      onSubmit={(e) => {
        e.preventDefault()
        add.mutate(undefined, { onSuccess: () => setListID('') })
      }}
    >
      <h3>Add to a list</h3>
      <select value={listID} onChange={(e) => setListID(e.target.value)} required aria-label="List">
        <option value="">Choose a list…</option>
        {lists.data?.lists
          .filter((list) => !subscribed.has(list.id))
          .map((list) => (
            <option key={list.id} value={list.id}>
              {list.name}
            </option>
          ))}
      </select>{' '}
      <button type="submit" disabled={add.isPending || !listID}>
        Add
      </button>
      {add.error && <p className="error">{errMessage(add.error)}</p>}
    </form>
  )
}

function DeleteMember({ member }: { member: Subscriber }) {
  const queryClient = useQueryClient()
  const remove = useMutation({
    mutationFn: () => apiSend('DELETE', `/api/members/${member.member_number}`),
    // The lookup then refetches and shows the member is gone.
    onSuccess: () => {
      queryClient.removeQueries({ queryKey: ['member', String(member.member_number)] })
      return queryClient.invalidateQueries({ queryKey: ['lists'] })
    },
  })

  function confirmDelete() {
    const question =
      `Delete member ${member.member_number} (${member.name}, ${member.email}) from newsletter-system and Listmonk?\n\n` +
      'This removes every subscription, opt-outs included. If they are still a member in orca, the next reconciliation proposes adding them back.'
    if (window.confirm(question)) remove.mutate()
  }

  return (
    <section className="danger">
      <h3>Delete member</h3>
      <button onClick={confirmDelete} disabled={remove.isPending}>
        Delete member {member.member_number}
      </button>
      {remove.error && <p className="error">{errMessage(remove.error)}</p>}
    </section>
  )
}
