import { fireEvent, render, screen } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { expect, it, vi } from 'vitest'
import { I18nProvider } from '@/lib/i18n'
import type { Container } from '@/lib/types'

const run = vi.fn()
vi.mock('@/components/change/ChangeDialog', () => ({ useChange: () => run }))
vi.mock('@/lib/auth', async (orig) => ({ ...(await orig<typeof import('@/lib/auth')>()), useAuth: () => ({ user: { role: 'super', perms: [], disabled: false } }) }))
vi.mock('@/lib/api', async (orig) => ({
  ...(await orig<typeof import('@/lib/api')>()),
  get: (p: string) => Promise.resolve(p.endsWith('/listening') ? { items: [{ port: 80, proto: 'tcp', loopback: false }] } : { items: [] }),
}))
import { ContainerList } from './ContainerList'

const ctr = (name: string, state: string): Container => ({
  id: name + 'id', name, image: 'nginx', image_id: '', state, status: state, created: 0, ports: [], networks: [], mounts: [], labels: {}, restart_count: 0, adopted: true,
})

it('shows state, internal ports and runs actions from the row menu', async () => {
  render(<I18nProvider initial="zh-CN"><MemoryRouter><ContainerList list={[ctr('n8n', 'running'), ctr('old', 'exited')]} reload={() => {}} onLogs={() => {}} /></MemoryRouter></I18nProvider>)
  expect(screen.getByText('n8n')).toHaveAttribute('data-state', 'running')
  expect(await screen.findByText('内部 80')).toBeInTheDocument()
  const trigger = screen.getAllByRole('button', { name: '更多' })[0]
  fireEvent.click(trigger)
  fireEvent.click(await screen.findByText('停止容器'))
  expect(run).toHaveBeenCalledWith('container.stop', { name: 'n8n' }, expect.objectContaining({ host: 'local' }))
})
