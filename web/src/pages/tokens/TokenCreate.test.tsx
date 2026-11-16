import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { expect, it, vi } from 'vitest'
import { I18nProvider } from '@/lib/i18n'

const post = vi.fn()
vi.mock('@/lib/api', async (orig) => ({ ...(await orig<typeof import('@/lib/api')>()), post: (...a: unknown[]) => post(...a) }))
import { TokenCreate, TokenShown } from './TokenCreate'

it('defaults Agent tokens to confirm-first, warns about never expiring, and posts scopes', async () => {
  const onCreated = vi.fn()
  post.mockResolvedValue({ token: 'berth_agt_x', item: { id: 1, kind: 'agt', name: 'claude' } })
  render(<I18nProvider initial="zh-CN"><TokenCreate resources={['containers', 'audit']} onClose={() => {}} onCreated={onCreated} /></I18nProvider>)
  expect(screen.getByRole('radio', { name: /危险操作先确认/ })).toBeChecked()
  fireEvent.change(screen.getByRole('combobox'), { target: { value: 'never' } })
  expect(screen.getByText(/永不过期的令牌/)).toBeInTheDocument()
  fireEvent.change(screen.getByLabelText('名称'), { target: { value: 'claude' } })
  fireEvent.click(screen.getByRole('button', { name: '创建令牌' }))
  await waitFor(() => expect(onCreated).toHaveBeenCalled())
  const body = post.mock.calls[0][1]
  expect(body).toMatchObject({ kind: 'agt', name: 'claude', danger_policy: 'confirm', expires_at: 0 })
  expect(body.scopes).toContain('containers:write')
})

it('shows the plaintext once with the MCP command', () => {
  render(<I18nProvider initial="zh-CN"><TokenShown token="berth_agt_abc" item={{ id: 1, kind: 'agt', name: 'claude' } as never} onClose={() => {}} /></I18nProvider>)
  expect(screen.getAllByTestId('equiv').some((e) => e.textContent?.includes('claude mcp add --transport http berth'))).toBe(true)
})
