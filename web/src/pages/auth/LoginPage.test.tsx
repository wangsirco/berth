import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { describe, expect, it, vi } from 'vitest'
import { ApiError } from '@/lib/api'
import { I18nProvider } from '@/lib/i18n'

const post = vi.fn()
vi.mock('@/lib/api', async (orig) => ({
  ...(await orig<typeof import('@/lib/api')>()),
  post: (...a: unknown[]) => post(...a),
  get: () => Promise.resolve({ needs_setup: false }),
}))
vi.mock('@/lib/auth', () => ({ useAuth: () => ({ user: null, refresh: async () => {} }) }))
import LoginPage from './LoginPage'

const ui = () => render(<I18nProvider initial="zh-CN"><MemoryRouter><LoginPage /></MemoryRouter></I18nProvider>)

describe('LoginPage', () => {
  it('asks for the code and submits six digits automatically', async () => {
    post.mockResolvedValueOnce({ stage: 'second_step', csrf: 'c', methods: ['totp', 'passkey'] })
      .mockRejectedValueOnce(new ApiError(401, 'auth.bad_code', '验证码不对或已用过'))
    ui()
    fireEvent.change(screen.getByLabelText('用户名'), { target: { value: 'iwsheng' } })
    fireEvent.change(screen.getByLabelText('密码'), { target: { value: 'correct horse 9' } })
    fireEvent.click(screen.getByRole('button', { name: '登录' }))
    const code = await screen.findByLabelText('验证码')
    fireEvent.change(code, { target: { value: '12 34 56' } })
    await waitFor(() => expect(post).toHaveBeenLastCalledWith('/api/v1/auth/totp', { code: '123456', remember: false }))
    expect(await screen.findByText('验证码不对或已用过')).toBeInTheDocument()
  })
})
