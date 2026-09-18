package mediacache

import (
	"strings"

	"github.com/geekjourneyx/md2wechat-skill/internal/config"
)

// AccountKey returns the cache isolation key for the active WeChat account:
// the named account when one is selected, otherwise the AppID.
func AccountKey(cfg *config.Config) string {
	if cfg == nil {
		return ""
	}
	if cfg.WechatAccountNamed {
		return strings.TrimSpace(cfg.WechatAccount)
	}
	return strings.TrimSpace(cfg.WechatAppID)
}
