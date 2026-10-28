package inbox

import "regexp"

// 线索正则（跨域契约口径，Go regexp）：取件码 / 验证码 / 运单号。
var (
	rePickupClue   = regexp.MustCompile(`(取件码|取货码|提货码|取件密码|菜鸟|驿站)[^\d]{0,12}(\d{4,8}([-]\d{1,4})?)`)
	reCodeClue     = regexp.MustCompile(`(验证码|校验码|动态码|确认码)[^\d]{0,10}(\d{4,8})`)
	reTrackingClue = regexp.MustCompile(`(运单号?|快递单号|物流单号)[^A-Za-z0-9]{0,8}([A-Za-z]{0,2}\d{10,15})`)
)

// ExtractClue 从通知文本提取线索，按 取件码>验证码>运单号 顺序取首个命中。
// 返回 clue_type（pickup|code|tracking）与 clue_value（捕获的号码串）；未命中返回空。
func ExtractClue(text string) (clueType, clueValue string) {
	for _, c := range [...]struct {
		typ string
		re  *regexp.Regexp
	}{
		{"pickup", rePickupClue},
		{"code", reCodeClue},
		{"tracking", reTrackingClue},
	} {
		if m := c.re.FindStringSubmatch(text); m != nil {
			return c.typ, m[2]
		}
	}
	return "", ""
}
