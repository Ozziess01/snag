// Package grouping решает, какие события — одна и та же проблема.
//
// От этого зависит, увидит человек в списке «TypeError — 10 000 раз» или
// десять тысяч отдельных строк. Отпечаток (fingerprint) строится так:
//
//  1. Если SDK прислал свой fingerprint, берём его; "{{ default }}" внутри
//     заменяется стандартным отпечатком.
//  2. Исключение со стектрейсом: тип исключения и кадры стека. Кадры —
//     только код приложения (in_app), если SDK их разметил. От кадра
//     берутся модуль или файл и функция, но не номер строки: иначе любая
//     правка выше по файлу создаёт «новую» проблему. Текст исключения
//     не участвует: «User 5 not found» и «User 7 not found» — одно и то же.
//  3. Исключение без стека: тип и текст, в котором числа, id, адреса
//     и прочие данные заменены заглушками.
//  4. Сообщение: шаблон до подстановки параметров или текст с заглушками.
package grouping

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"regexp"
	"strings"

	"github.com/Ozziess01/snag/internal/event"
)

// Version входит в хеш. Если алгоритм поменяется, старые и новые события
// не склеятся по ошибке, а переход можно будет сделать осознанно.
const Version = "snag:1"

// Сколько кадров учитывать: для группировки важен конец стека, у места
// падения, а длинные цепочки фреймворка сверху ничего не добавляют.
const maxFrames = 30

// Result — отпечаток и то, из чего он собран. Parts показываются в
// интерфейсе в ответ на вопрос «почему эти события склеились».
type Result struct {
	Hash  uint64
	Kind  string // custom, stacktrace, exception, message, fallback
	Parts []string
}

func (r Result) Hex() string {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], r.Hash)
	return hex.EncodeToString(b[:])
}

func Compute(e *event.Event) Result {
	kind, parts := defaultParts(e)
	if len(e.Fingerprint) > 0 {
		var custom []string
		for _, f := range e.Fingerprint {
			if isDefaultMarker(f) {
				custom = append(custom, parts...)
				continue
			}
			custom = append(custom, f)
		}
		kind, parts = "custom", custom
	}
	return Result{Hash: hash(kind, parts), Kind: kind, Parts: parts}
}

func defaultParts(e *event.Event) (string, []string) {
	if len(e.Exception) > 0 {
		var parts []string
		withStack := false
		for _, ex := range e.Exception {
			frames := frameKeys(ex.Stacktrace)
			if len(frames) > 0 {
				withStack = true
			}
			parts = append(parts, "type:"+exceptionType(ex))
			parts = append(parts, frames...)
		}
		if withStack {
			return "stacktrace", parts
		}
		// Ни у одного исключения нет стека — группируем по типу и тексту.
		parts = parts[:0]
		for _, ex := range e.Exception {
			parts = append(parts, "type:"+exceptionType(ex), "value:"+Parameterize(ex.Value))
		}
		return "exception", parts
	}

	if msg := messageTemplate(e); msg != "" {
		parts := []string{"message:" + msg}
		if e.Logger != "" {
			parts = append(parts, "logger:"+e.Logger)
		}
		return "message", parts
	}
	return "fallback", []string{"title:" + Parameterize(e.Title()), "culprit:" + e.Culprit()}
}

func exceptionType(ex event.Exception) string {
	if ex.Type != "" {
		return ex.Type
	}
	return "<unknown>"
}

// messageTemplate: шаблон logentry/message, если SDK его прислал, иначе
// текст с заглушками вместо данных.
func messageTemplate(e *event.Event) string {
	if e.LogEntry != nil && e.LogEntry.Message != "" {
		return e.LogEntry.Message
	}
	if e.Message.Message != "" {
		return e.Message.Message
	}
	if e.LogEntry != nil && e.LogEntry.Formatted != "" {
		return Parameterize(e.LogEntry.Formatted)
	}
	return Parameterize(e.Message.Formatted)
}

// frameKeys превращает стек в список «где: функция». Порядок кадров
// в протоколе от старого вызова к месту падения, так и оставляем.
func frameKeys(st *event.Stacktrace) []string {
	if st == nil || len(st.Frames) == 0 {
		return nil
	}
	frames := st.Frames
	if app := inAppOnly(frames); len(app) > 0 {
		frames = app
	}

	keys := make([]string, 0, len(frames))
	for _, f := range frames {
		k := frameKey(f)
		if k == "" {
			continue
		}
		// Рекурсия: глубина меняется от вызова к вызову, а проблема одна.
		if n := len(keys); n > 0 && keys[n-1] == k {
			continue
		}
		keys = append(keys, k)
	}
	if len(keys) > maxFrames {
		keys = keys[len(keys)-maxFrames:]
	}
	return keys
}

func inAppOnly(frames []event.Frame) []event.Frame {
	var out []event.Frame
	for _, f := range frames {
		if f.InApp != nil && *f.InApp {
			out = append(out, f)
		}
	}
	return out
}

func frameKey(f event.Frame) string {
	where := f.Module
	if where == "" {
		where = normalizeFilename(f.Filename)
	}
	if where == "" {
		where = normalizeFilename(f.AbsPath)
	}

	fn := normalizeFunction(f.Function)
	if fn == "" {
		// Безымянная функция (частый случай в JS): вместо неё строка кода.
		// Она переживает правки выше по файлу лучше, чем номер строки.
		if line := strings.TrimSpace(f.ContextLine); line != "" && len(line) <= 120 {
			fn = "line:" + line
		}
	}
	if where == "" && fn == "" {
		return ""
	}
	return "frame:" + where + ":" + fn
}

var (
	reQuery       = regexp.MustCompile(`[?#].*$`)
	reOrigin      = regexp.MustCompile(`^[a-z][a-z0-9+.-]*://[^/]*`)
	reAssetHash   = regexp.MustCompile(`([.\-_])[0-9a-f]{6,}(\.[a-z0-9]+)$`)
	reVersionDir  = regexp.MustCompile(`/v?\d+\.\d+(\.\d+)*[^/]*/`)
	reClosureNum  = regexp.MustCompile(`\bfunc\d+\b`)
	reAnonymousJS = regexp.MustCompile(`^(Object\.)?<anonymous>$`)
)

// normalizeFilename убирает из пути то, что меняется между сборками
// и серверами, но не меняет суть: хост, query, хеш в имени бандла,
// папку с версией.
func normalizeFilename(name string) string {
	name = strings.ReplaceAll(name, `\`, "/")
	name = reQuery.ReplaceAllString(name, "")
	name = reOrigin.ReplaceAllString(name, "")
	name = reVersionDir.ReplaceAllString(name, "/<version>/")
	name = reAssetHash.ReplaceAllString(name, "$1<hash>$2")
	return name
}

func normalizeFunction(fn string) string {
	fn = strings.TrimSpace(fn)
	fn = strings.TrimPrefix(fn, "async ")
	switch {
	case fn == "?", fn == "<unknown>", reAnonymousJS.MatchString(fn):
		return ""
	}
	// Замыкания в Go нумеруются по порядку (main.func1, main.func2):
	// добавишь одно выше — номера у остальных сдвинутся.
	return reClosureNum.ReplaceAllString(fn, "func")
}

func isDefaultMarker(s string) bool {
	return strings.ReplaceAll(s, " ", "") == "{{default}}"
}

func hash(kind string, parts []string) uint64 {
	h := sha256.New()
	h.Write([]byte(Version))
	h.Write([]byte{0})
	h.Write([]byte(kind))
	for _, p := range parts {
		h.Write([]byte{0})
		h.Write([]byte(p))
	}
	return binary.BigEndian.Uint64(h.Sum(nil)[:8])
}
