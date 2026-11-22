import { lazy, Suspense } from 'react'
import { Navigate, Route, Routes } from 'react-router-dom'
import { RequireAuth, Shell } from './components/shell/Shell'
import { useT } from './lib/i18n'
import { useAuth } from './lib/auth'

// Home sends people who have not finished the welcome wizard there first.
function Home() {
  const { user } = useAuth()
  if (user?.simple) return <Navigate to="/wall" replace />
  return <Navigate to={user && !(user as { onboarded?: boolean }).onboarded ? '/welcome' : '/local/overview'} replace />
}

const Login = lazy(() => import('./pages/auth/LoginPage'))
const Setup = lazy(() => import('./pages/auth/SetupPage'))
const CliAuth = lazy(() => import('./pages/auth/CliAuthPage'))
const Welcome = lazy(() => import('./pages/welcome/Welcome'))
const Account = lazy(() => import('./pages/account/AccountPage'))
const Overview = lazy(() => import('./pages/overview/OverviewPage'))
const Containers = lazy(() => import('./pages/containers/ContainersPage'))
const Files = lazy(() => import('./pages/files/FilesPage'))
const Trash = lazy(() => import('./pages/files/TrashPage'))
const Audit = lazy(() => import('./pages/audit/AuditPage'))
const Users = lazy(() => import('./pages/users/UsersPage'))
const Tokens = lazy(() => import('./pages/tokens/TokensPage'))
const Settings = lazy(() => import('./pages/settings/SettingsPage'))
const Terminal = lazy(() => import('./pages/terminal/TerminalPage'))
const Sites = lazy(() => import('./pages/sites/SitesPage'))
const Projects = lazy(() => import('./pages/projects/ProjectsPage'))
const Domains = lazy(() => import('./pages/domains/DomainsPage'))
const Cloudflare = lazy(() => import('./pages/cloudflare/CloudflarePage'))
const Cron = lazy(() => import('./pages/cron/CronPage'))
const WharfHome = lazy(() => import('./pages/wharf/WharfPage'))
const Pipelines = lazy(() => import('./pages/pipeline/PipelinePage'))
const Hosts = lazy(() => import('./pages/hosts/HostsPage'))
const Store = lazy(() => import('./pages/store/StorePage'))
const Databases = lazy(() => import('./pages/db/DatabasesPage'))
const Backup = lazy(() => import('./pages/backup/BackupPage'))
const Monitor = lazy(() => import('./pages/monitor/MonitorPage'))
const Network = lazy(() => import('./pages/network/NetworkPage'))
const WharfPaired = lazy(() => import('./pages/network/WharfPaired'))
const Vms = lazy(() => import('./pages/vms/VmsPage'))
const Dev = lazy(() => import('./pages/dev/DevPage'))
const Remote = lazy(() => import('./pages/remote/RemotePage'))
const System = lazy(() => import('./pages/system/SystemPage'))
const RemoteViewer = lazy(() => import('./pages/remote/ViewerPage'))
const VmScreen = lazy(() => import('./pages/vms/ScreenPage'))
const PveScreen = lazy(() => import('./pages/vms/PveScreenPage'))
const AcceptInvite = lazy(() => import('./pages/invite/AcceptInvitePage'))
const SharedViewer = lazy(() => import('./pages/remote/SharedViewer'))
const Proxy = lazy(() => import('./pages/proxy/ProxyPage'))
const NotFound = lazy(() => import('./pages/NotFound'))
const Plugins = lazy(() => import('./pages/plugins/PluginsPage'))
const ExtensionView = lazy(() => import('./pages/extensions/ExtensionsPage').then((m) => ({ default: m.ExtensionView })))
const Wall = lazy(() => import('./pages/wall/WallPage'))
const Screen = lazy(() => import('./pages/screen/ScreenPage'))

export default function App() {
  const t = useT()
  return (
    <Suspense fallback={<div className="p-6 text-mute">{t('common.loading')}</div>}>
      <Routes>
        <Route path="/login" element={<Login />} />
        <Route path="/setup/:code" element={<Setup />} />
        <Route path="/accept-invite" element={<AcceptInvite />} />
        <Route path="/shared" element={<SharedViewer />} />
        <Route path="/cli-auth" element={<RequireAuth><CliAuth /></RequireAuth>} />
        <Route path="/welcome" element={<RequireAuth><Welcome /></RequireAuth>} />
        <Route path="/remote/:id" element={<RequireAuth><RemoteViewer /></RequireAuth>} />
        <Route path="/vm-screen/:host/:name" element={<RequireAuth><VmScreen /></RequireAuth>} />
        <Route path="/pve-screen/:cluster/:node/:type/:vmid" element={<RequireAuth><PveScreen /></RequireAuth>} />
        <Route path="/wharf/paired" element={<RequireAuth><WharfPaired /></RequireAuth>} />
        <Route path="/" element={<RequireAuth><Home /></RequireAuth>} />
        <Route path="/wall" element={<RequireAuth><Wall /></RequireAuth>} />
        <Route path="/screen" element={<RequireAuth><Screen /></RequireAuth>} />
        <Route path="/account" element={<RequireAuth><Shell /></RequireAuth>}>
          <Route index element={<Account />} />
        </Route>
        <Route path="/:host" element={<RequireAuth><Shell /></RequireAuth>}>
          <Route index element={<Navigate to="overview" replace />} />
          <Route path="overview" element={<Overview />} />
          <Route path="sites/*" element={<Sites />} />
          <Route path="projects/*" element={<Projects />} />
          <Route path="domains/*" element={<Domains />} />
          <Route path="cloudflare/*" element={<Cloudflare />} />
          <Route path="proxy" element={<Proxy />} />
          <Route path="cron" element={<Cron />} />
          <Route path="wharf" element={<WharfHome />} />
          <Route path="store/*" element={<Store />} />
          <Route path="db" element={<Databases />} />
          <Route path="backup/*" element={<Backup />} />
          <Route path="monitor" element={<Monitor />} />
          <Route path="network" element={<Network />} />
          <Route path="vms" element={<Vms />} />
          <Route path="dev" element={<Dev />} />
          <Route path="remote" element={<Remote />} />
          <Route path="system" element={<System />} />
          <Route path="pipeline/*" element={<Pipelines />} />
          <Route path="hosts" element={<Hosts />} />
          <Route path="containers/*" element={<Containers />} />
          <Route path="files/*" element={<Files />} />
          <Route path="files-trash" element={<Trash />} />
          <Route path="audit" element={<Audit />} />
          <Route path="users" element={<Users />} />
          <Route path="tokens" element={<Tokens />} />
          <Route path="settings/*" element={<Settings />} />
          <Route path="plugins/*" element={<Plugins />} />
          <Route path="extensions" element={<Navigate to="../plugins" replace />} />
          <Route path="ext/:id" element={<ExtensionView />} />
          <Route path="terminal" element={<Terminal />} />
          <Route path="*" element={<NotFound />} />
        </Route>
      </Routes>
    </Suspense>
  )
}
