// The parts of noVNC's RFB client the consoles use (PVE, VM screen).
declare module '@novnc/novnc' {
  export default class RFB extends EventTarget {
    constructor(target: HTMLElement, urlOrChannel: string | WebSocket, options?: { credentials?: { password?: string; username?: string; target?: string }; wsProtocols?: string[] })
    scaleViewport: boolean
    clipViewport: boolean
    showDotCursor: boolean
    resizeSession: boolean
    viewOnly: boolean
    focusOnClick: boolean
    background: string
    disconnect(): void
    sendCtrlAltDel(): void
    sendCredentials(c: { password?: string; username?: string }): void
    focus(): void
    blur(): void
    sendKey(keysym: number, code: string | null, down?: boolean): void
    toDataURL(type?: string): string
  }
}
