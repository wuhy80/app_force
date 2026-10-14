package agentbundle

import (
	"fmt"
	"io"
)

type Platform struct {
	OS     string `json:"os"`
	Arch   string `json:"arch"`
	File   string `json:"file"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

func Open(goos, arch string) (io.ReadCloser, Platform, error) {
	for _, platform := range Platforms() {
		if platform.OS == goos && platform.Arch == arch {
			reader, err := openPayload(platform.File)
			return reader, platform, err
		}
	}
	return nil, Platform{}, fmt.Errorf("此安装包未包含 %s/%s Agent，请使用包含远程 Agent 的完整构建", goos, arch)
}
