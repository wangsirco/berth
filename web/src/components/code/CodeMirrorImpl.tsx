import { defaultKeymap, history, historyKeymap, indentWithTab } from '@codemirror/commands'
import { css } from '@codemirror/lang-css'
import { html } from '@codemirror/lang-html'
import { javascript } from '@codemirror/lang-javascript'
import { json } from '@codemirror/lang-json'
import { markdown } from '@codemirror/lang-markdown'
import { python } from '@codemirror/lang-python'
import { yaml } from '@codemirror/lang-yaml'
import { bracketMatching, defaultHighlightStyle, foldGutter, HighlightStyle, indentOnInput, syntaxHighlighting } from '@codemirror/language'
import { highlightSelectionMatches, searchKeymap } from '@codemirror/search'
import { Compartment, EditorState, type Extension } from '@codemirror/state'
import { drawSelection, EditorView, highlightActiveLine, highlightActiveLineGutter, keymap, lineNumbers } from '@codemirror/view'
import { tags } from '@lezer/highlight'
import { useEffect, useRef } from 'react'
import { useTheme } from '@/lib/theme'
import type { CodeEditorProps } from './CodeEditor'

const langs: Record<string, () => Extension> = {
  yaml: yaml, json: json, js: () => javascript(), ts: () => javascript({ typescript: true }), py: python, md: markdown, html: html, css: css,
}

const darkHighlight = HighlightStyle.define([
  { tag: tags.keyword, color: '#ff7b72' }, { tag: [tags.string, tags.special(tags.string)], color: '#a5d6ff' },
  { tag: tags.number, color: '#79c0ff' }, { tag: tags.comment, color: '#8b949e', fontStyle: 'italic' },
  { tag: [tags.propertyName, tags.attributeName], color: '#7ee2d6' }, { tag: tags.bool, color: '#79c0ff' },
  { tag: tags.typeName, color: '#ffa657' }, { tag: tags.definition(tags.variableName), color: '#d2a8ff' },
])

function base(dark: boolean): Extension {
  return [
    EditorView.theme({
      '&': { fontSize: '12.5px', backgroundColor: 'var(--canvas)', color: 'var(--fg)', height: '100%' },
      '.cm-content': { fontFamily: 'var(--font-mono)', caretColor: 'var(--fg)' },
      '.cm-gutters': { backgroundColor: 'var(--soft)', color: 'var(--mute)', borderRight: '1px solid var(--line)' },
      '.cm-activeLine': { backgroundColor: 'color-mix(in srgb, var(--acc) 6%, transparent)' },
      '.cm-activeLineGutter': { backgroundColor: 'color-mix(in srgb, var(--acc) 10%, transparent)' },
      '&.cm-focused .cm-selectionBackground, .cm-selectionBackground': { backgroundColor: 'color-mix(in srgb, var(--acc) 22%, transparent) !important' },
      '.cm-scroller': { overflow: 'auto' },
    }, { dark }),
    syntaxHighlighting(dark ? darkHighlight : defaultHighlightStyle),
  ]
}

export default function CodeMirrorImpl({ value, onChange, language = 'text', readOnly, height = '420px' }: CodeEditorProps) {
  const host = useRef<HTMLDivElement>(null)
  const view = useRef<EditorView>(null)
  const theme = useRef(new Compartment())
  const change = useRef(onChange)
  change.current = onChange
  const { dark } = useTheme()

  useEffect(() => {
    const v = new EditorView({
      parent: host.current!,
      state: EditorState.create({
        doc: value,
        extensions: [
          lineNumbers(), highlightActiveLineGutter(), foldGutter(), history(), drawSelection(), indentOnInput(), bracketMatching(),
          highlightActiveLine(), highlightSelectionMatches(),
          keymap.of([...defaultKeymap, ...historyKeymap, ...searchKeymap, indentWithTab]),
          langs[language]?.() ?? [],
          theme.current.of(base(dark)),
          EditorState.readOnly.of(!!readOnly),
          EditorView.updateListener.of((u) => { if (u.docChanged) change.current?.(u.state.doc.toString()) }),
        ],
      }),
    })
    view.current = v
    return () => v.destroy()
    // the editor owns its text after creation; external value changes reset it below
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [language, readOnly])

  useEffect(() => {
    const v = view.current
    if (v && v.state.doc.toString() !== value) v.dispatch({ changes: { from: 0, to: v.state.doc.length, insert: value } })
  }, [value])

  useEffect(() => { view.current?.dispatch({ effects: theme.current.reconfigure(base(dark)) }) }, [dark])

  return <div ref={host} style={{ height }} className="overflow-hidden rounded-[var(--radius-box)] border border-line" />
}
