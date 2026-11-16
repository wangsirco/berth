import { fireEvent, render, screen } from '@testing-library/react'
import { expect, it, vi } from 'vitest'
import { I18nProvider } from '@/lib/i18n'
import { SessionList, type Session } from './TerminalPanel'

it('lists sessions running in the background and re-attaches', () => {
  const onAttach = vi.fn()
  const s: Session = { id: 'pty_1', target: 'host', user: 'root', title: 'mini', created: 0, last_active: Date.now() / 1000, clients: 0, recording: false, exited: false }
  render(<I18nProvider initial="zh-CN"><SessionList sessions={[s]} open={[]} onAttach={onAttach} onKill={() => {}} /></I18nProvider>)
  expect(screen.getByText(/后台运行中/)).toBeInTheDocument()
  fireEvent.click(screen.getByText('接上'))
  expect(onAttach).toHaveBeenCalledWith(s)
})
