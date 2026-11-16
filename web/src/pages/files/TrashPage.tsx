import { useChange } from '@/components/change/ChangeDialog'
import { useHost } from '@/components/shell/host'
import { Box, Empty, Row } from '@/components/ui/Box'
import { Button } from '@/components/ui/Button'
import { bytes, datetime } from '@/lib/format'
import { useApi } from '@/lib/hooks'
import { useT } from '@/lib/i18n'

export default function TrashPage() {
  const t = useT()
  const run = useChange()
  const { host } = useHost()
  const l = useApi<{ items: { id: string; paths: string[]; deleted: number; size: number }[] }>(`/api/v1/hosts/${host}/files/trash`, [host])
  return (
    <Box title={t('files.trash_title')} count={l.data?.items.length}>
      {l.data?.items.length === 0 && <Empty title={t('files.trash_empty')}>{t('files.trash_hint')}</Empty>}
      {l.data?.items.map((x) => (
        <Row key={x.id}>
          <div className="min-w-0 flex-1">
            <div className="num truncate text-[13px]">{x.paths.join(', ')}</div>
            <div className="text-[12px] text-mute">{datetime(x.deleted)} · {bytes(x.size)}</div>
          </div>
          <Button size="sm" onClick={() => run('file.restore', { id: x.id, paths: x.paths }, { host, onDone: () => void l.reload() })}>{t('action.file.restore')}</Button>
          <Button size="sm" variant="danger" onClick={() => run('file.purge', { id: x.id, paths: x.paths }, { host, onDone: () => void l.reload(), confirmName: x.paths[0].split('/').pop() })}>{t('action.file.purge')}</Button>
        </Row>
      ))}
    </Box>
  )
}
