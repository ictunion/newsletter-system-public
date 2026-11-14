import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { type FormEvent, useState } from 'react'
import { apiGet, apiSend, errMessage } from '../api/client'
import type { ListsResponse, NewsletterList, Rule, RulesResponse } from '../api/types'

export function RulesPage() {
  const queryClient = useQueryClient()
  const [editing, setEditing] = useState<string>() // rule ID, or 'new'
  const rules = useQuery({ queryKey: ['rules'], queryFn: () => apiGet<RulesResponse>('/api/rules') })
  const lists = useQuery({
    queryKey: ['lists', { includeDeleted: true }],
    queryFn: () => apiGet<ListsResponse>('/api/lists?include_deleted=true'),
  })
  const listByID = new Map(lists.data?.lists.map((list) => [list.id, list]))
  // Grouped by list, so e.g. "praha" and "prague" rules for one list sit
  // together; within a list, "all members" first, then cities A-Z.
  const listName = (rule: Rule) => listByID.get(rule.list_id)?.name ?? ''
  const sortedRules = [...(rules.data?.rules ?? [])].sort(
    (a, b) =>
      listName(a).localeCompare(listName(b), 'cs') ||
      (a.kind === b.kind ? 0 : a.kind === 'all' ? -1 : 1) ||
      (a.city_contains ?? '').localeCompare(b.city_contains ?? '', 'cs'),
  )
  const remove = useMutation({
    mutationFn: (id: string) => apiSend('DELETE', `/api/rules/${id}`),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['rules'] }),
  })

  return (
    <>
      <h2>Rules</h2>
      <p className="muted">
        Rules put members on lists: every matching enabled rule applies. City rules match when the member's city contains
        the text, ignoring case and accents. Changes apply to new and updated members right away; for existing members,
        the daily orca reconciliation proposes them as pending changes.
      </p>
      {rules.error && <p className="error">{errMessage(rules.error)}</p>}
      {remove.error && <p className="error">{errMessage(remove.error)}</p>}
      <table>
        <thead>
          <tr>
            <th>List</th>
            <th>Matches</th>
            <th>Enabled</th>
            <th>Note</th>
            <th />
          </tr>
        </thead>
        <tbody>
          {sortedRules.map((rule) =>
            editing === rule.id ? (
              <tr key={rule.id}>
                <td colSpan={5}>
                  <RuleForm rule={rule} lists={lists.data?.lists ?? []} onDone={() => setEditing(undefined)} />
                </td>
              </tr>
            ) : (
              <tr key={rule.id} className={rule.enabled ? undefined : 'deleted'}>
                <td>
                  {listByID.get(rule.list_id)?.name ?? rule.list_id}
                  {listByID.get(rule.list_id)?.deleted_at && ' (deleted list: rule has no effect)'}
                </td>
                <td>{rule.kind === 'all' ? 'all members' : `city contains "${rule.city_contains}"`}</td>
                <td>{rule.enabled ? 'yes' : 'no'}</td>
                <td>{rule.note}</td>
                <td className="actions">
                  <button onClick={() => setEditing(rule.id)}>Edit</button>
                  <button
                    disabled={remove.isPending}
                    onClick={() => {
                      if (window.confirm('Delete this rule? Subscriptions it created stay.')) remove.mutate(rule.id)
                    }}
                  >
                    Delete
                  </button>
                </td>
              </tr>
            ),
          )}
        </tbody>
      </table>
      {editing === 'new' ? (
        <RuleForm lists={lists.data?.lists ?? []} onDone={() => setEditing(undefined)} />
      ) : (
        <button onClick={() => setEditing('new')}>New rule</button>
      )}
    </>
  )
}

// RuleForm creates a rule, or replaces rule when given.
function RuleForm({ rule, lists, onDone }: { rule?: Rule; lists: NewsletterList[]; onDone: () => void }) {
  const queryClient = useQueryClient()
  const [kind, setKind] = useState<Rule['kind']>(rule?.kind ?? 'city')
  const [listID, setListID] = useState(rule?.list_id ?? '')
  const [city, setCity] = useState(rule?.city_contains ?? '')
  const [enabled, setEnabled] = useState(rule?.enabled ?? true)
  const [note, setNote] = useState(rule?.note ?? '')

  const save = useMutation({
    mutationFn: () => {
      const body = { kind, list_id: listID, city_contains: kind === 'city' ? city : null, enabled, note: note || null }
      return rule ? apiSend<Rule>('PUT', `/api/rules/${rule.id}`, body) : apiSend<Rule>('POST', '/api/rules', body)
    },
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ['rules'] })
      onDone()
    },
  })

  function submit(e: FormEvent) {
    e.preventDefault()
    save.mutate()
  }

  return (
    <form onSubmit={submit} className="rule-form">
      <label>
        List{' '}
        <select value={listID} onChange={(e) => setListID(e.target.value)} required>
          <option value="">Choose a list…</option>
          {lists
            .filter((list) => !list.deleted_at || list.id === rule?.list_id)
            .map((list) => (
              <option key={list.id} value={list.id}>
                {list.name}
              </option>
            ))}
        </select>
      </label>
      <label>
        Matches{' '}
        <select value={kind} onChange={(e) => setKind(e.target.value as Rule['kind'])}>
          <option value="city">members whose city contains</option>
          <option value="all">all members</option>
        </select>
      </label>
      {kind === 'city' && (
        <label>
          City text <input value={city} onChange={(e) => setCity(e.target.value)} required placeholder="praha" />
        </label>
      )}
      <label>
        <input type="checkbox" checked={enabled} onChange={(e) => setEnabled(e.target.checked)} /> Enabled
      </label>
      <label>
        Note <input value={note} onChange={(e) => setNote(e.target.value)} />
      </label>
      <span className="actions">
        <button type="submit" disabled={save.isPending}>
          {rule ? 'Save' : 'Create rule'}
        </button>
        <button type="button" onClick={onDone}>
          Cancel
        </button>
      </span>
      {save.error && <p className="error">{errMessage(save.error)}</p>}
    </form>
  )
}
