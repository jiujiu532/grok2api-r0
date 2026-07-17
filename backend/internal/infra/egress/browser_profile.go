package egress

import (
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/bogdanfinn/tls-client/profiles"
)

var (
	chromeVersionPattern  = regexp.MustCompile(`(?i)(?:chrome|chromium|crios)/(\d+)`)
	firefoxVersionPattern = regexp.MustCompile(`(?i)firefox/(\d+)`)
	edgeVersionPattern    = regexp.MustCompile(`(?i)edg(?:e|a|ios)?/(\d+)`)
	safariVersionPattern  = regexp.MustCompile(`(?i)version/(\d+)`)
)

// chromeMajors 为 tls-client v1.15 中可用的 Chrome 主版本（升序）。
var chromeMajors = []int{103, 104, 105, 106, 107, 108, 109, 110, 111, 112, 116, 117, 120, 124, 130, 131, 133, 144, 146}

var chromeProfiles = map[int]profiles.ClientProfile{
	103: profiles.Chrome_103,
	104: profiles.Chrome_104,
	105: profiles.Chrome_105,
	106: profiles.Chrome_106,
	107: profiles.Chrome_107,
	108: profiles.Chrome_108,
	109: profiles.Chrome_109,
	110: profiles.Chrome_110,
	111: profiles.Chrome_111,
	112: profiles.Chrome_112,
	116: profiles.Chrome_116_PSK,
	117: profiles.Chrome_117,
	120: profiles.Chrome_120,
	124: profiles.Chrome_124,
	130: profiles.Chrome_130_PSK,
	131: profiles.Chrome_131,
	133: profiles.Chrome_133,
	144: profiles.Chrome_144,
	146: profiles.Chrome_146,
}

var firefoxProfiles = map[int]profiles.ClientProfile{
	102: profiles.Firefox_102,
	104: profiles.Firefox_104,
	105: profiles.Firefox_105,
	106: profiles.Firefox_106,
	108: profiles.Firefox_108,
	110: profiles.Firefox_110,
	117: profiles.Firefox_117,
	120: profiles.Firefox_120,
	123: profiles.Firefox_123,
	132: profiles.Firefox_132,
	133: profiles.Firefox_133,
	135: profiles.Firefox_135,
	147: profiles.Firefox_147,
	148: profiles.Firefox_148,
}

// ClientProfileFromUserAgent 将 UA 映射到最接近的 tls-client 浏览器指纹。
func ClientProfileFromUserAgent(userAgent string) profiles.ClientProfile {
	ua := strings.TrimSpace(userAgent)
	if ua == "" {
		return profiles.Chrome_146
	}
	lower := strings.ToLower(ua)

	if match := firefoxVersionPattern.FindStringSubmatch(lower); len(match) == 2 {
		if profile, ok := nearestProfile(parseMajor(match[1]), firefoxProfiles); ok {
			return profile
		}
		return profiles.Firefox_148
	}

	// Edge 走 Chromium TLS 指纹；优先按 Chrome 版本对齐。
	if edgeVersionPattern.MatchString(lower) {
		if match := chromeVersionPattern.FindStringSubmatch(lower); len(match) == 2 {
			return nearestChromeProfile(parseMajor(match[1]))
		}
		return profiles.Chrome_146
	}

	if match := chromeVersionPattern.FindStringSubmatch(lower); len(match) == 2 {
		return nearestChromeProfile(parseMajor(match[1]))
	}

	if strings.Contains(lower, "safari/") && !strings.Contains(lower, "chrome/") && !strings.Contains(lower, "chromium/") {
		if strings.Contains(lower, "iphone") || strings.Contains(lower, "ipad") {
			if match := safariVersionPattern.FindStringSubmatch(lower); len(match) == 2 {
				switch major := parseMajor(match[1]); {
				case major >= 18:
					return profiles.Safari_IOS_18_5
				case major >= 17:
					return profiles.Safari_IOS_17_0
				case major >= 16:
					return profiles.Safari_IOS_16_0
				default:
					return profiles.Safari_IOS_15_6
				}
			}
			return profiles.Safari_IOS_18_5
		}
		return profiles.Safari_16_0
	}

	return profiles.Chrome_146
}

// ApplyClientHints 写入与 UA 对齐的 Sec-Ch-Ua 系列请求头（仅 Chromium 系）。
func ApplyClientHints(header http.Header, userAgent string) {
	if header == nil {
		return
	}
	hints := clientHints(userAgent)
	for key, value := range hints {
		header.Set(key, value)
	}
}

func clientHints(userAgent string) map[string]string {
	ua := strings.TrimSpace(userAgent)
	if ua == "" {
		ua = DefaultUserAgent
	}
	lower := strings.ToLower(ua)
	if strings.Contains(lower, "firefox/") {
		return nil
	}
	if strings.Contains(lower, "safari/") && !strings.Contains(lower, "chrome/") && !strings.Contains(lower, "chromium/") && !strings.Contains(lower, "edg") {
		return nil
	}

	major := 0
	brand := "Google Chrome"
	if edgeVersionPattern.MatchString(lower) {
		brand = "Microsoft Edge"
		if match := chromeVersionPattern.FindStringSubmatch(lower); len(match) == 2 {
			major = parseMajor(match[1])
		} else if match := edgeVersionPattern.FindStringSubmatch(lower); len(match) == 2 {
			major = parseMajor(match[1])
		}
	} else if match := chromeVersionPattern.FindStringSubmatch(lower); len(match) == 2 {
		major = parseMajor(match[1])
	}
	if major <= 0 {
		major = 146
	}

	platform := clientHintPlatform(lower)
	mobile := "?0"
	if strings.Contains(lower, "mobile") || platform == "Android" || platform == "iOS" {
		mobile = "?1"
	}
	arch := clientHintArch(lower)

	result := map[string]string{
		"Sec-Ch-Ua":        fmt.Sprintf(`"%s";v="%d", "Chromium";v="%d", "Not(A:Brand";v="24"`, brand, major, major),
		"Sec-Ch-Ua-Mobile": mobile,
		"Sec-Ch-Ua-Model":  `""`,
	}
	if platform != "" {
		result["Sec-Ch-Ua-Platform"] = `"` + platform + `"`
	}
	if arch != "" {
		result["Sec-Ch-Ua-Arch"] = `"` + arch + `"`
		result["Sec-Ch-Ua-Bitness"] = `"64"`
	}
	return result
}

func clientHintPlatform(lowerUA string) string {
	switch {
	case strings.Contains(lowerUA, "windows"):
		return "Windows"
	case strings.Contains(lowerUA, "mac os x"), strings.Contains(lowerUA, "macintosh"):
		return "macOS"
	case strings.Contains(lowerUA, "android"):
		return "Android"
	case strings.Contains(lowerUA, "iphone"), strings.Contains(lowerUA, "ipad"):
		return "iOS"
	case strings.Contains(lowerUA, "linux"):
		return "Linux"
	default:
		return ""
	}
}

func clientHintArch(lowerUA string) string {
	switch {
	case strings.Contains(lowerUA, "aarch64"), strings.Contains(lowerUA, "arm"):
		return "arm"
	case strings.Contains(lowerUA, "x86_64"), strings.Contains(lowerUA, "x64"), strings.Contains(lowerUA, "win64"), strings.Contains(lowerUA, "intel"):
		return "x86"
	default:
		return ""
	}
}

func nearestChromeProfile(major int) profiles.ClientProfile {
	if profile, ok := nearestProfile(major, chromeProfiles); ok {
		return profile
	}
	return profiles.Chrome_146
}

func nearestProfile(major int, available map[int]profiles.ClientProfile) (profiles.ClientProfile, bool) {
	if major <= 0 || len(available) == 0 {
		return profiles.ClientProfile{}, false
	}
	if profile, ok := available[major]; ok {
		return profile, true
	}
	bestMajor := -1
	for candidate := range available {
		if candidate <= major && candidate > bestMajor {
			bestMajor = candidate
		}
	}
	if bestMajor < 0 {
		// UA 比库内最新还新时，取最高版本。
		for candidate := range available {
			if candidate > bestMajor {
				bestMajor = candidate
			}
		}
	}
	profile, ok := available[bestMajor]
	return profile, ok
}

func parseMajor(value string) int {
	major, err := strconv.Atoi(value)
	if err != nil {
		return 0
	}
	return major
}
