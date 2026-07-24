package executor

import (
	"regexp"
	"strings"
)

const maxRedactedBytes = 4096

var secretPatterns = []struct {
	re          *regexp.Regexp
	replacement string
}{
	{regexp.MustCompile(`(?i)(bearer\s+)[A-Za-z0-9._~+\-/=]+`), `${1}[REDACTED]`},
	{regexp.MustCompile(`(?im)^([ \t]*(?:export[ \t]+)?[A-Z0-9_]*(?:API[_-]?KEY|TOKEN|SECRET|PASSWORD|PASSWD)[A-Z0-9_]*[ \t]*=[ \t]*).*$`), `${1}[REDACTED]`},
	{regexp.MustCompile(`(?i)("(?:api[_-]?key|token|secret|password|passwd)"\s*:\s*")[^"]*(")`), `${1}[REDACTED]${2}`},
	{regexp.MustCompile(`(?i)((?:api[_-]?key|token|secret|password|passwd)\s*[:=]\s*)[^\s,;]+`), `${1}[REDACTED]`},
}

// Redact 脱敏文本中的密钥与令牌。
func Redact(value string) string {
	redacted := value
	for _, pattern := range secretPatterns {
		redacted = pattern.re.ReplaceAllString(redacted, pattern.replacement)
	}
	if len(redacted) > maxRedactedBytes {
		redacted = strings.ToValidUTF8(redacted[:maxRedactedBytes], "")
	}
	return redacted
}
