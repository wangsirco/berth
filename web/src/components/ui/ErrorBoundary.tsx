// Adapted from github.com/wangsirco/wharf (web/src/components/error-boundary.tsx), Copyright (c) Wharf authors, MIT License.
import { Component, type ReactNode } from 'react'
import { translate, pickLocale } from '@/lib/i18n'

export class ErrorBoundary extends Component<{ children: ReactNode }, { error: Error | null }> {
  state = { error: null as Error | null }
  static getDerivedStateFromError(error: Error) { return { error } }
  render() {
    if (!this.state.error) return this.props.children
    const l = pickLocale(localStorage.getItem('berth.locale') ?? '', navigator.language)
    return (
      <div className="mx-auto mt-24 max-w-lg rounded-[var(--radius-box)] border border-line p-6">
        <h1 className="text-base font-semibold">{translate(l, 'common.error_title')}</h1>
        <pre className="num mt-3 overflow-auto whitespace-pre-wrap text-xs text-mute">{this.state.error.message}</pre>
        <button className="mt-4 rounded-[var(--radius-box)] border border-line px-3 py-1 text-sm" onClick={() => location.reload()}>
          {translate(l, 'common.reload_page')}
        </button>
      </div>
    )
  }
}
