package event

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"
)

// Filtered — чем заменяется вычищенное значение (как у Sentry).
const Filtered = "[Filtered]"

// Имена полей, значения которых нельзя хранить: пароли, токены, ключи,
// cookie. Сравнение по вхождению подстроки без учёта регистра, так что
// «user_password», «X-Api-Key» и «sessionid» тоже попадают.
var sensitiveKeys = []string{
	"password", "passwd", "pwd", "secret", "token", "api_key", "apikey", "api-key",
	"auth", "credential", "private_key", "privatekey", "session", "csrf", "xsrf",
	"cookie", "card_number", "cardnumber", "cvv", "cvc",
}

// Значения, похожие на секрет, даже если ключ безобидный.
var sensitiveValues = []*regexp.Regexp{
	regexp.MustCompile(`(?i)^(bearer|basic|token)\s+\S+`),
	// Номер карты: 13–19 цифр, возможно с пробелами или дефисами.
	regexp.MustCompile(`^\d(?:[ -]?\d){12,18}$`),
}

func sensitiveKey(k string) bool {
	k = strings.ToLower(k)
	for _, s := range sensitiveKeys {
		if strings.Contains(k, s) {
			return true
		}
	}
	return false
}

// Scrub вычищает секреты из JSON события до записи в базу: всё, что лежит
// под «опасными» ключами на любой глубине, и значения, похожие на токены
// и номера карт. Заголовки в форме [["Cookie","…"]] обрабатываются так же,
// как объекты. Если JSON не разобрался, возвращается как есть: событие
// всё равно прошло Decode и будет принято.
func Scrub(raw []byte) []byte {
	var v any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if dec.Decode(&v) != nil {
		return raw
	}
	out, err := json.Marshal(scrubValue(v, false))
	if err != nil {
		return raw
	}
	return out
}

func scrubValue(v any, hide bool) any {
	switch t := v.(type) {
	case map[string]any:
		for k, child := range t {
			t[k] = scrubValue(child, hide || sensitiveKey(k))
		}
		return t
	case []any:
		// Пара [ключ, значение] — так SDK присылают заголовки и теги.
		if len(t) == 2 {
			if k, ok := t[0].(string); ok && sensitiveKey(k) {
				t[1] = Filtered
				return t
			}
		}
		for i, child := range t {
			t[i] = scrubValue(child, hide)
		}
		return t
	case string:
		if hide && t != "" {
			return Filtered
		}
		for _, re := range sensitiveValues {
			if re.MatchString(t) {
				return Filtered
			}
		}
		return t
	case json.Number, bool:
		if hide {
			return Filtered
		}
		return t
	default:
		return t
	}
}
