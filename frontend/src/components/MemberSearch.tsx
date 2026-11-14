import { type FormEvent, useState } from 'react'
import { useNavigate } from 'react-router-dom'

// Header search: a member number opens that member's page.
export function MemberSearch() {
  const navigate = useNavigate()
  const [number, setNumber] = useState('')

  function search(e: FormEvent) {
    e.preventDefault()
    navigate(`/members?number=${number.trim()}`)
    setNumber('')
  }

  return (
    <form onSubmit={search} className="member-search">
      <input
        value={number}
        onChange={(e) => setNumber(e.target.value)}
        placeholder="Member #"
        aria-label="Member number"
        inputMode="numeric"
        pattern="\s*\d+\s*"
        required
      />
    </form>
  )
}
