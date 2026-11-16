import { useEffect, useRef } from 'react'
import { subscribe } from './events'

export function useEvents(types: string[], fn: (type: string, data: any) => void) {
  const ref = useRef(fn)
  ref.current = fn
  const key = types.join(',')
  useEffect(() => subscribe((t, d) => { if (key.split(',').includes(t)) ref.current(t, d) }), [key])
}
