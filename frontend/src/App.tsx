import { useAuth } from 'react-oidc-context'
import { Link, Route, Routes } from 'react-router-dom'
import { AuthGate } from './AuthGate'

export function App() {
  return (
    <AuthGate>
      <Dashboard />
    </AuthGate>
  )
}

function Dashboard() {
  const auth = useAuth()

  return (
    <main>
      <header
        style={{
          display: 'flex',
          justifyContent: 'space-between',
          alignItems: 'baseline',
          gap: '1rem',
          color: '#fff',
          padding: '1rem 2rem',
        }}
      >
        <div style={{ display: 'flex', alignItems: 'baseline', gap: '1.5rem' }}>
          <h1 style={{ margin: 0, fontSize: '1.25rem' }}>Newsletter System — Admin</h1>
          {/* Gate links with useHasRole('<role>') from ./roles. */}
          <nav style={{ display: 'flex', gap: '1rem' }}>
            <Link to="/" style={{ color: '#fff' }}>
              Home
            </Link>
          </nav>
        </div>
        <span>
          {auth.user?.profile.email}{' '}
          <button onClick={() => void auth.signoutRedirect()}>Sign out</button>
        </span>
      </header>
      <div style={{ padding: '0 2rem 2rem' }}>
        <div style={{ background: '#fff', borderRadius: '8px', padding: '1.5rem' }}>
          <Routes>
            <Route path="/" element={<p>Welcome.</p>} />
          </Routes>
        </div>
      </div>
    </main>
  )
}
