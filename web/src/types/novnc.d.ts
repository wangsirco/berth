// The parts of noVNC's RFB client the PVE console uses.
declare module '@novnc/novnc' {
  export default class RFB extends EventTarget {
    constructor(target: HTMLElement, urlOrChannel: string | WebSocket, options?: { credentials?: { password?: string; username?: string; target?: string }; wsProtocols?: string[] })
    scaleViewport: boolean
    resizeSession: boolean
    viewOnly: boolean
    focusOnClick: boolean
    background: string
    disconnect(): void
    sendCtrlAltDel(): void
    sendCredentials(c: { password?: string; username?: string }): void
    focus(): void
  }
}
