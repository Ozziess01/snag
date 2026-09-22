package event

import (
	"encoding/json"
	"net/url"
	"regexp"
	"strings"
)

// DerivedTags — теги, которые Sentry вычисляет на сервере сам: браузер,
// ОС, среда выполнения, окружение, версия. По ним интерфейс показывает
// разбивку «70 % ошибок в Chrome». Явные теги от SDK важнее: вычисленный
// тег не перезаписывает присланный.
func DerivedTags(e *Event) map[string]string {
	tags := make(map[string]string, len(e.Tags)+8)
	for _, kv := range e.Tags {
		tags[kv[0]] = kv[1]
	}
	set := func(k, v string) {
		v = strings.TrimSpace(v)
		if v == "" {
			return
		}
		if _, ok := tags[k]; !ok {
			tags[k] = truncate(v, maxTagValueLen)
		}
	}

	set("level", e.Level)
	set("environment", e.Environment)
	set("release", e.Release)
	set("server_name", e.ServerName)
	set("transaction", e.Transaction)

	for _, name := range []string{"browser", "os", "runtime", "device"} {
		set(name, contextNameVersion(e.Contexts[name]))
	}
	if e.Request != nil {
		if ua, ok := e.Request.Headers.GetFold("User-Agent"); ok {
			browser, os := ParseUserAgent(ua)
			set("browser", browser)
			set("os", os)
		}
		if u, err := url.Parse(e.Request.URL); err == nil && u.Path != "" {
			set("url", u.Scheme+"://"+u.Host+u.Path)
		}
	}
	if e.User != nil && e.User.ID != "" {
		set("user", "id:"+string(e.User.ID))
	}
	return tags
}

// contextNameVersion: {"name":"Chrome","version":"128.0.1"} → «Chrome 128».
func contextNameVersion(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var c struct {
		Name    string `json:"name"`
		Version string `json:"version"`
		Model   string `json:"model"`
	}
	if json.Unmarshal(raw, &c) != nil {
		return ""
	}
	if c.Name == "" {
		return c.Model
	}
	return strings.TrimSpace(c.Name + " " + majorVersion(c.Version))
}

func majorVersion(v string) string {
	v = strings.TrimPrefix(v, "v")
	if i := strings.IndexAny(v, ".-_ "); i > 0 {
		return v[:i]
	}
	return v
}

// GetFold — значение по ключу без учёта регистра (заголовки HTTP).
func (p Pairs) GetFold(key string) (string, bool) {
	for _, kv := range p {
		if strings.EqualFold(kv[0], key) {
			return kv[1], true
		}
	}
	return "", false
}

// ---------- User-Agent ----------

var (
	// Порядок важен: Edge, Opera и Яндекс притворяются Chrome, а Chrome —
	// Safari, поэтому более специфичные проверяются раньше.
	uaBrowsers = []struct {
		name string
		re   *regexp.Regexp
	}{
		{"Yandex Browser", regexp.MustCompile(`YaBrowser/(\d+)`)},
		{"Edge", regexp.MustCompile(`Edg(?:e|A|iOS)?/(\d+)`)},
		{"Opera", regexp.MustCompile(`(?:OPR|Opera)/(\d+)`)},
		{"Samsung Internet", regexp.MustCompile(`SamsungBrowser/(\d+)`)},
		{"Firefox", regexp.MustCompile(`(?:Firefox|FxiOS)/(\d+)`)},
		{"Chrome", regexp.MustCompile(`(?:Chrome|CriOS)/(\d+)`)},
		{"Safari", regexp.MustCompile(`Version/(\d+)[.\d]* (?:Mobile/\S+ )?Safari/`)},
	}
	uaHeadless = regexp.MustCompile(`HeadlessChrome/(\d+)`)
	uaWindows  = regexp.MustCompile(`Windows NT (\d+\.\d+)`)
	uaAndroid  = regexp.MustCompile(`Android (\d+)`)
	uaIOS      = regexp.MustCompile(`(?:iPhone|iPad|iPod).*? OS (\d+)`)
	uaMac      = regexp.MustCompile(`Mac OS X (\d+)[_.](\d+)`)
)

// ParseUserAgent выделяет браузер и ОС с мажорной версией:
// «Chrome 128», «Windows 10». Полноценный парсер (сотни правил) тут
// не нужен: для разбивки по тегам хватает популярных браузеров.
func ParseUserAgent(ua string) (browser, os string) {
	if m := uaHeadless.FindStringSubmatch(ua); m != nil {
		browser = "HeadlessChrome " + m[1]
	} else {
		for _, b := range uaBrowsers {
			if m := b.re.FindStringSubmatch(ua); m != nil {
				browser = b.name + " " + m[1]
				break
			}
		}
	}

	switch {
	case uaWindows.MatchString(ua):
		// Windows 11 тоже пишет «NT 10.0», отличить их по User-Agent нельзя.
		switch uaWindows.FindStringSubmatch(ua)[1] {
		case "10.0":
			os = "Windows 10"
		case "6.3":
			os = "Windows 8.1"
		case "6.1":
			os = "Windows 7"
		default:
			os = "Windows"
		}
	case uaAndroid.MatchString(ua):
		os = "Android " + uaAndroid.FindStringSubmatch(ua)[1]
	case uaIOS.MatchString(ua):
		os = "iOS " + uaIOS.FindStringSubmatch(ua)[1]
	case uaMac.MatchString(ua):
		m := uaMac.FindStringSubmatch(ua)
		os = "macOS " + m[1]
		if m[1] == "10" {
			os += "." + m[2]
		}
	case strings.Contains(ua, "Linux"):
		os = "Linux"
	}
	return browser, os
}
