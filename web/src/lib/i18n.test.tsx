import { render, screen } from '@testing-library/react'
import { expect, it } from 'vitest'
import { I18nProvider, useT, pickLocale } from './i18n'
import zhCN from '../i18n/zh-CN.json'
import zhTW from '../i18n/zh-TW.json'
import en from '../i18n/en.json'

it('picks locale from navigator', () => {
  expect(pickLocale('', 'zh-HK')).toBe('zh-TW')
  expect(pickLocale('', 'zh-Hant-TW')).toBe('zh-TW')
  expect(pickLocale('', 'zh-SG')).toBe('zh-CN')
  expect(pickLocale('', 'fr-FR')).toBe('en')
  expect(pickLocale('en', 'zh-CN')).toBe('en')
})

it('interpolates params', () => {
  function X() { const t = useT(); return <span>{t('common.minutes_ago', { n: 3 })}</span> }
  render(<I18nProvider initial="zh-CN"><X /></I18nProvider>)
  expect(screen.getByText('3 分钟前')).toBeTruthy()
})

it('all locales have the same keys', () => {
  const k = (o: object) => Object.keys(o).sort()
  expect(k(zhTW)).toEqual(k(zhCN))
  expect(k(en)).toEqual(k(zhCN))
})
