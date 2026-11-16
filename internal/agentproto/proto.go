// Package agentproto holds types shared by berth-panel and berth agent.
package agentproto

type Caps struct {
	OS, OSVersion, Kernel, Arch, DockerVersion     string
	PageSize                                       int
	Docker, Compose, Incus, KVM, Smartctl, TC, Nft bool
	Missing                                        []string // 缺失的依赖包名，供「一键安装依赖」
}

type ErrBody struct {
	Error struct {
		Code   string         `json:"code"`
		Params map[string]any `json:"params,omitempty"`
	} `json:"error"`
}
