package superagent

import "strings"

// Filter 是调用模型之前的确定性 Fast Path。空配置只做最基本的完整性过滤。
type Filter struct {
	// EnabledChannels 非空时表示只接收这些渠道；direct 用户聊天与 trusted 输入不受限制。
	EnabledChannels map[string]bool
	IgnoreKinds     map[string]bool
	IgnorePrivacy   map[string]bool
}

func (f Filter) Check(in TriggerInput) (bool, string) {
	if in.Trusted {
		return false, ""
	}
	if len(f.EnabledChannels) > 0 && !f.EnabledChannels[in.Channel] {
		return true, "channel disabled"
	}
	if len(f.IgnoreKinds) > 0 && f.IgnoreKinds[in.Kind] {
		return true, "kind ignored"
	}
	if len(f.IgnorePrivacy) > 0 && f.IgnorePrivacy[strings.TrimSpace(in.PrivacyLevel)] {
		return true, "privacy level ignored"
	}
	return false, ""
}
