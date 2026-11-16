// The languages a native project can be written in, and how the UI names them.
export type Kind = 'node' | 'java' | 'go' | 'python'
export const KINDS: Kind[] = ['node', 'java', 'go', 'python']
export const LANG: Record<Kind, string> = { node: 'Node', java: 'Java', go: 'Go', python: 'Python' }
export const langOf = (k?: string) => LANG[(k || 'node') as Kind] ?? 'Node'
