import { act, fireEvent, render, screen } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { describe, expect, it, vi } from 'vitest'
import { I18nProvider } from '@/lib/i18n'
import { ThemeProvider } from '@/lib/theme'

vi.mock('@/lib/useEvents', () => ({ useEvents: () => {} }))
vi.mock('@/lib/auth', async (orig) => ({
  ...(await orig<typeof import('@/lib/auth')>()),
  useAuth: () => ({ user: { id: 1, username: 'iwsheng', display_name: 'W', role: 'super', perms: [], scope: {}, disabled: false }, loading: false }),
}))
vi.mock('@/lib/api', async (orig) => ({
  ...(await orig<typeof import('@/lib/api')>()),
  get: (p: string) => Promise.resolve(
    p.startsWith('/api/v1/hosts?') || p === '/api/v1/hosts' ? { items: [{ name: 'local', online: true }], name: 'mini' }
      : p.includes('/statusbar') ? { online: true, queue: { running: 0, waiting: 0 }, pending: 0, version: 'dev' }
        : { items: [] }),
}))

import { Shell } from './Shell'

function ui() {
  return render(
    <I18nProvider initial="zh-CN"><ThemeProvider>
      <MemoryRouter initialEntries={['/local/overview']}>
        <Routes><Route path="/:host" element={<Shell />}><Route path="overview" element={<input aria-label="field" />} /></Route></Routes>
      </MemoryRouter>
    </ThemeProvider></I18nProvider>,
  )
}

describe('Shell', () => {
  it('shows the grouped sidebar and marks the current page', async () => {
    ui()
    const nav = await screen.findByRole('navigation', { name: '主导航' })
    for (const n of ['首页', '我的东西', '网站', '应用与容器', '虚拟机', '数据库', '文件', '保护', '备份', '服务器', '系统']) {
      expect(nav).toHaveTextContent(n)
    }
    expect(screen.getByRole('link', { name: '首页' })).toHaveAttribute('aria-current', 'page')
  })

  it('backtick toggles the terminal drawer, except while typing', async () => {
    ui()
    await screen.findByRole('navigation', { name: '主导航' })
    const field = screen.getByLabelText('field')
    field.focus()
    fireEvent.keyDown(field, { key: '`' })
    expect(screen.queryByRole('dialog')).toBeNull()
    field.blur()
    await act(async () => { fireEvent.keyDown(window, { key: '`' }) })
    expect(await screen.findByRole('dialog')).toBeInTheDocument()
  })
})
