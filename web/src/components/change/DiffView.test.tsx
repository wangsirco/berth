import { render } from '@testing-library/react'
import { expect, it } from 'vitest'
import { DiffView, parseUnified } from './DiffView'

const diff = `--- a/etc/x
+++ b/etc/x
@@ -1,4 +1,5 @@
 keep
-old1
-old2
+new1
+new2
+new3
 tail
`

it('parses hunks with line numbers', () => {
  const p = parseUnified(diff)
  expect(p.path).toBe('/etc/x')
  expect(p.hunks).toHaveLength(1)
  const l = p.hunks[0].lines
  expect(l.filter((x) => x.op === '-')).toHaveLength(2)
  expect(l.filter((x) => x.op === '+')).toHaveLength(3)
  expect(l[l.length - 1]).toMatchObject({ op: ' ', a: 4, b: 5 })
})

it('renders rows by operation', () => {
  const { container } = render(<DiffView diff={diff} />)
  expect(container.querySelectorAll('[data-hunk]')).toHaveLength(1)
  expect(container.querySelectorAll('[data-op="-"]')).toHaveLength(2)
  expect(container.querySelectorAll('[data-op="+"]')).toHaveLength(3)
})
