import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { I18nProvider } from '@/lib/i18n'
import { ChangeProvider, useChange } from './ChangeDialog'

const post = vi.fn()
vi.mock('@/lib/api', async (orig) => ({ ...(await orig<typeof import('@/lib/api')>()), post: (...a: unknown[]) => post(...a) }))
vi.mock('@/lib/useEvents', () => ({ useEvents: () => {} }))

const l10n = (zh: string) => ({ 'zh-CN': zh, 'zh-TW': zh, en: zh })
const plan = (risk: string, status = 'planned') => ({
  id: 'chg_1', kind: 'container.stop', status, params: { name: 'n8n' }, equivalent: 'berth container stop n8n',
  title: l10n('停止容器'), summary: l10n('停止容器 n8n。'), action: l10n('停止容器'),
  plan: { risk, undoable: true, affected: [{ type: 'container', id: 'n8n', name: 'n8n' }], files: [], commands: ['docker stop n8n'] },
})

function Harness() {
  const run = useChange()
  return <button onClick={() => run('container.stop', { name: 'n8n' })}>go</button>
}
const ui = () => render(<I18nProvider initial="zh-CN"><ChangeProvider><Harness /></ChangeProvider></I18nProvider>)

describe('ChangeDialog', () => {
  beforeEach(() => post.mockReset())

  it('previews, shows the equivalent command and risk shape, then runs', async () => {
    post.mockResolvedValueOnce(plan('medium')).mockResolvedValueOnce(plan('medium', 'done'))
    ui()
    fireEvent.click(screen.getByText('go'))
    expect(await screen.findByText('停止容器 n8n', { selector: 'button' })).toBeInTheDocument()
    expect(screen.getByTestId('equiv')).toHaveTextContent('berth container stop n8n')
    expect(document.querySelector('[data-shape="triangle"]')).not.toBeNull()
    expect(screen.getByText('中风险')).toBeInTheDocument()
    expect(post.mock.calls[0][0]).toBe('/api/v1/hosts/local/changes/container.stop?plan=1')
    fireEvent.click(screen.getByTestId('confirm'))
    await waitFor(() => expect(post).toHaveBeenCalledTimes(2))
    expect(post.mock.calls[1][0]).toBe('/api/v1/hosts/local/changes/container.stop')
  })

  it('destructive changes need the name typed', async () => {
    post.mockResolvedValueOnce(plan('destructive'))
    ui()
    fireEvent.click(screen.getByText('go'))
    const btn = await screen.findByTestId('confirm')
    await waitFor(() => expect(btn).toBeDisabled())
    fireEvent.change(screen.getByLabelText('输入 n8n 以确认'), { target: { value: 'n8n' } })
    expect(btn).toBeEnabled()
  })
})
