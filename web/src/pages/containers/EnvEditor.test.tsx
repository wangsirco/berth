import { fireEvent, render, screen } from '@testing-library/react'
import { expect, it, vi } from 'vitest'
import { I18nProvider } from '@/lib/i18n'
import { EnvEditor } from './EnvEditor'

it('masks secrets and keeps order and comments when editing', () => {
  const onChange = vi.fn()
  const text = '# app\nA=1\nDB_PASSWORD=xyz\n'
  render(<I18nProvider initial="en"><EnvEditor value={text} onChange={onChange} /></I18nProvider>)
  expect(document.querySelectorAll('[data-masked]')).toHaveLength(1)
  expect(screen.queryByDisplayValue('xyz')).toBeNull()
  fireEvent.change(screen.getByDisplayValue('1'), { target: { value: '2' } })
  expect(onChange).toHaveBeenCalledWith('# app\nA=2\nDB_PASSWORD=xyz\n')
})
