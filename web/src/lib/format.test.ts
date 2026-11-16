import { describe, expect, it } from 'vitest'
import { ago, bytes, duration, rate } from './format'
import { translate } from './i18n'

const t = (k: string, p?: Record<string, unknown>) => translate('zh-CN', k, p)

describe('format', () => {
  it('bytes', () => {
    expect(bytes(1536)).toBe('1.5 KB')
    expect(bytes(5.2 * 2 ** 30)).toBe('5.2 GB')
    expect(bytes(0)).toBe('0 B')
  })
  it('rate', () => expect(rate(3355443)).toBe('3.2 MB/s'))
  it('duration', () => expect(duration(1051200, t)).toBe('12 天 4 小时'))
  it('ago', () => expect(ago(1000, t, 1000 + 180)).toBe('3 分钟前'))
})
