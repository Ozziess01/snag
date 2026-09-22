package grouping

import "regexp"

// Заглушки вместо данных в текстах ошибок. Порядок важен: сначала длинные
// и специфичные шаблоны (URL, UUID), потом общие (числа), иначе «число»
// откусит кусок UUID.
var params = []struct {
	re  *regexp.Regexp
	sub string
}{
	{regexp.MustCompile(`\b[a-z][a-z0-9+.-]*://[^\s'"<>)]+`), "<url>"},
	{regexp.MustCompile(`[\w.+-]+@[\w-]+\.[\w.-]+`), "<email>"},
	{regexp.MustCompile(`(?i)\b[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\b`), "<uuid>"},
	{regexp.MustCompile(`\b\d{4}-\d{2}-\d{2}([ T]\d{2}:\d{2}(:\d{2}(\.\d+)?)?(Z|[+-]\d{2}:?\d{2})?)?\b`), "<date>"},
	{regexp.MustCompile(`\b\d{1,3}(\.\d{1,3}){3}(:\d+)?\b`), "<ip>"},
	{regexp.MustCompile(`(?i)\b0x[0-9a-f]+\b`), "<hex>"},
	// Хеши и токены: длинная hex-строка, в которой есть хотя бы одна цифра
	// (чтобы не заменить обычное слово вроде «deadbeef»… почти обычное).
	{regexp.MustCompile(`(?i)\b[0-9a-f]*\d[0-9a-f]*[a-f][0-9a-f]*\b|\b[0-9a-f]*[a-f][0-9a-f]*\d[0-9a-f]*\b`), "<hex>"},
	{regexp.MustCompile(`-?\b\d+(\.\d+)?\b`), "<int>"},
}

// Parameterize заменяет в тексте данные заглушками:
// «Order 17 for bob@example.com failed» → «Order <int> for <email> failed».
func Parameterize(s string) string {
	for _, p := range params {
		s = p.re.ReplaceAllStringFunc(s, func(m string) string {
			// Короткие hex-похожие слова (a1, 3d) оставляем числам и словам.
			if p.sub == "<hex>" && len(m) < 8 && !(len(m) > 2 && (m[1] == 'x' || m[1] == 'X')) {
				return m
			}
			return p.sub
		})
	}
	return s
}
