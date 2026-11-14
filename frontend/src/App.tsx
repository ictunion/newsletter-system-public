import { useAuth } from 'react-oidc-context'
import { Link, Route, Routes } from 'react-router-dom'
import { AuthGate } from './AuthGate'
import { MemberSearch } from './components/MemberSearch'
import { PendingChangesLabel } from './components/PendingChangesLabel'
import { AuditLogPage } from './pages/AuditLogPage'
import { ChangesPage } from './pages/ChangesPage'
import { JobsPage } from './pages/JobsPage'
import { RulesPage } from './pages/RulesPage'
import { DashboardPage } from './pages/DashboardPage'
import { ListSubscribersPage } from './pages/ListSubscribersPage'
import { ListsPage } from './pages/ListsPage'
import { MemberPage } from './pages/MemberPage'
import { ROLE_MANAGE_RULES, ROLE_VIEW_AUDIT_LOGS, ROLE_VIEW_NEWSLETTERS, useHasRole } from './roles'

export function App() {
  return (
    <AuthGate>
      <Layout />
    </AuthGate>
  )
}

function Layout() {
  const auth = useAuth()
  const canView = useHasRole(ROLE_VIEW_NEWSLETTERS)
  const canViewAudit = useHasRole(ROLE_VIEW_AUDIT_LOGS)
  // The rules page also needs view-newsletters, for the list names.
  const canManageRules = useHasRole(ROLE_MANAGE_RULES) && canView

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
            {canView && (
              <>
                <Link to="/" style={{ color: '#fff' }}>
                  Dashboard
                </Link>
                <Link to="/lists" style={{ color: '#fff' }}>
                  Newsletters
                </Link>
                <Link to="/changes" style={{ color: '#fff' }}>
                  <PendingChangesLabel />
                </Link>
              </>
            )}
            {canManageRules && (
              <Link to="/rules" style={{ color: '#fff' }}>
                Rules
              </Link>
            )}
            {canViewAudit && (
              <Link to="/audit-log" style={{ color: '#fff' }}>
                Audit log
              </Link>
            )}
          </nav>
        </div>
        <span style={{ display: 'flex', alignItems: 'baseline', gap: '0.75rem' }}>
          {canView && <MemberSearch />}
          {auth.user?.profile.email}
          <button onClick={() => void auth.signoutRedirect()}>Sign out</button>
        </span>
      </header>
      <div style={{ padding: '0 2rem 2rem' }}>
        <div style={{ background: '#fff', borderRadius: '8px', padding: '1.5rem' }}>
          <Routes>
            {canView && (
              <>
                <Route path="/" element={<DashboardPage />} />
                <Route path="/lists" element={<ListsPage />} />
                <Route path="/lists/:id" element={<ListSubscribersPage />} />
                <Route path="/members" element={<MemberPage />} />
                <Route path="/changes" element={<ChangesPage />} />
                <Route path="/jobs" element={<JobsPage />} />
              </>
            )}
            {canManageRules && <Route path="/rules" element={<RulesPage />} />}
            {canViewAudit && <Route path="/audit-log" element={<AuditLogPage />} />}
            <Route
              path="*"
              element={
                canView || canViewAudit ? (
                  <p>Page not found.</p>
                ) : (
                  <p>You have no access to newsletters. Ask an admin for the view-newsletters role.</p>
                )
              }
            />
          </Routes>
        </div>
      </div>
    </main>
  )
}
