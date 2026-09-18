package wechat

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var errCodePattern = regexp.MustCompile(`\b([0-9]{5})\b`)

// 微信失效类错误码：media_id 无效或素材已被删除。
const (
	// ErrCodeInvalidMediaID 不合法的 media_id / 素材不存在。
	ErrCodeInvalidMediaID = 40007
	// ErrCodeInvalidImage 图片素材无效。
	ErrCodeInvalidImage = 40009
)

var invalidMediaCodes = map[int]struct{}{
	ErrCodeInvalidMediaID: {},
	ErrCodeInvalidImage:   {},
}

func isInvalidMediaCode(code int) bool {
	_, ok := invalidMediaCodes[code]
	return ok
}

// MediaErrorCode extracts the five-digit WeChat errcode embedded in err, when present.
func MediaErrorCode(err error) (int, bool) {
	if err == nil {
		return 0, false
	}
	matches := errCodePattern.FindStringSubmatch(err.Error())
	if len(matches) < 2 {
		return 0, false
	}
	code, convErr := strconv.Atoi(matches[1])
	if convErr != nil {
		return 0, false
	}
	return code, true
}

// IsInvalidMediaError reports whether err is a WeChat response indicating that a
// media_id is invalid or has been deleted. It is a pure observation used to drive
// record invalidation; it never mutates state.
func IsInvalidMediaError(err error) bool {
	code, ok := MediaErrorCode(err)
	if !ok {
		return false
	}
	return isInvalidMediaCode(code)
}

// ExplainDraftAPIError converts known WeChat draft API errors into actionable hints.
func ExplainDraftAPIError(code int, msg string) string {
	base := fmt.Sprintf("wechat api error: %d - %s", code, msg)
	hint := draftAPIErrorHint(code)
	if hint == "" {
		return base
	}
	return base + "\nhint: " + hint
}

// ExplainDraftError inspects a raw error and enriches it when a known WeChat errcode is present.
func ExplainDraftError(err error) error {
	if err == nil {
		return nil
	}

	message := err.Error()
	matches := errCodePattern.FindStringSubmatch(message)
	if len(matches) < 2 {
		return err
	}
	code, convErr := strconv.Atoi(matches[1])
	if convErr != nil {
		return err
	}
	hint := draftAPIErrorHint(code)
	if hint == "" || strings.Contains(message, "\nhint: ") {
		return err
	}
	return fmt.Errorf("%s\nhint: %s", message, hint)
}

func draftAPIErrorHint(code int) string {
	switch code {
	case 45002:
		return "draft content exceeds the WeChat limit; shorten the article body or reduce oversized embedded HTML."
	case 45003:
		return "draft title exceeds the WeChat limit; shorten --title or frontmatter.title to 32 characters or fewer."
	case 45004:
		return "draft digest/description exceeds the WeChat limit; shorten --digest or frontmatter digest/summary/description to 128 characters or fewer."
	case 45005:
		return "a link field in the draft payload is invalid or exceeds the WeChat limit; check content_source_url or any generated external link fields."
	default:
		return ""
	}
}
