// Команда snag-bench — нагрузочный тест приёма.
//
//	snag-bench -dsn http://<key>@localhost:8000/1 -c 64 -d 30s
//	snag-bench -dsn ... -total 1000000        # ровно миллион событий
//	snag-bench -dsn ... -demo                 # залить историю демо-магазина
//
// Шлёт реалистичные конверты (~2 КБ: стек, крошки, теги, контексты,
// 200 разных проблем, разные браузеры и пользователи) с -c параллельных
// соединений. Каждый отправленный конверт уникален: свой event_id,
// пользователь, номер заказа в тексте и время — иначе ClickHouse сожмёт
// повторы и замер места будет нечестным. Печатает:
//   - сколько событий в секунду принял приём и с какой задержкой ответа;
//   - сколько из них отбито 429 (очередь полна);
//   - за сколько воркер дописал всё принятое в Postgres и ClickHouse
//     (по счётчикам /healthz) — это и есть сквозная пропускная способность.
package main

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Ozziess01/snag/internal/demo"
)

func main() {
	dsn := flag.String("dsn", "", "DSN проекта: http://<key>@host:port/<project>")
	conc := flag.Int("c", 64, "параллельных соединений")
	dur := flag.Duration("d", 30*time.Second, "длительность (если -total не задан)")
	total := flag.Int64("total", 0, "отправить ровно столько событий и остановиться")
	issues := flag.Int("issues", 200, "сколько разных проблем в потоке")
	demoMode := flag.Bool("demo", false, "не нагружать, а залить историю демо-магазина за две недели")
	flag.Parse()

	u, err := url.Parse(*dsn)
	if err != nil || u.User == nil {
		fmt.Fprintln(os.Stderr, "нужен -dsn вида http://<key>@localhost:8000/1")
		os.Exit(2)
	}
	key := u.User.Username()
	project := u.Path[1:]
	base := u.Scheme + "://" + u.Host
	target := fmt.Sprintf("%s/api/%s/envelope/?sentry_key=%s&sentry_version=7", base, project, key)

	if *demoMode {
		sendDemo(target)
		return
	}

	fmt.Printf("готовлю конверты: %d проблем...\n", *issues)
	pool := buildPool(5000, *issues)
	avg := 0
	for _, b := range pool {
		avg += len(b)
	}
	fmt.Printf("средний размер конверта: %.1f КБ\n", float64(avg)/float64(len(pool))/1024)

	client := &http.Client{
		Timeout:   30 * time.Second,
		Transport: &http.Transport{MaxIdleConns: *conc * 2, MaxIdleConnsPerHost: *conc * 2, MaxConnsPerHost: *conc * 2},
	}
	startWritten, _ := health(client, base)

	var (
		sent, ok, busy, failed atomic.Int64
		mu                     sync.Mutex
		latencies              []time.Duration
		wg                     sync.WaitGroup
	)
	deadline := time.Now().Add(*dur)
	start := time.Now()
	for w := 0; w < *conc; w++ {
		wg.Add(1)
		go func(seed uint64) {
			defer wg.Done()
			r := rand.New(rand.NewPCG(seed, 7))
			local := make([]time.Duration, 0, 1<<16)
			for {
				if *total > 0 {
					if sent.Add(1) > *total {
						break
					}
				} else if time.Now().After(deadline) {
					break
				} else {
					sent.Add(1)
				}
				body := personalize(pool[r.IntN(len(pool))], r)
				t0 := time.Now()
				resp, err := client.Post(target, "text/plain;charset=UTF-8", bytes.NewReader(body))
				d := time.Since(t0)
				if err != nil {
					failed.Add(1)
					continue
				}
				_, _ = io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
				switch resp.StatusCode {
				case 200:
					ok.Add(1)
					local = append(local, d)
				case 429:
					busy.Add(1)
				default:
					failed.Add(1)
				}
			}
			mu.Lock()
			latencies = append(latencies, local...)
			mu.Unlock()
		}(uint64(w) + 1)
	}
	wg.Wait()
	elapsed := time.Since(start)

	slices.Sort(latencies)
	pct := func(p float64) time.Duration {
		if len(latencies) == 0 {
			return 0
		}
		return latencies[int(float64(len(latencies)-1)*p)]
	}
	fmt.Printf("\nприём за %s, %d соединений:\n", elapsed.Round(time.Millisecond), *conc)
	fmt.Printf("  принято:  %d событий, %.0f в секунду\n", ok.Load(), float64(ok.Load())/elapsed.Seconds())
	fmt.Printf("  429:      %d (очередь полна, SDK повторил бы позже)\n", busy.Load())
	fmt.Printf("  ошибки:   %d\n", failed.Load())
	fmt.Printf("  задержка: p50 %s, p95 %s, p99 %s, max %s\n", pct(.50).Round(10*time.Microsecond), pct(.95).Round(10*time.Microsecond), pct(.99).Round(10*time.Microsecond), pct(1).Round(time.Microsecond))

	// Ждём, пока воркер допишет очередь в базы.
	accepted := ok.Load()
	var written int64
	for i := 0; i < 600; i++ {
		w, queue := health(client, base)
		written = w - startWritten
		if queue == 0 && written >= accepted {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	e2e := time.Since(start)
	fmt.Printf("\nзаписано в базы: %d событий за %s — %.0f в секунду сквозь всё (приём → группировка → Postgres → ClickHouse)\n",
		written, e2e.Round(time.Millisecond), float64(written)/e2e.Seconds())
}

// Заглушки в шаблоне конверта, которые personalize заменяет при отправке.
// Одинаковой длины с заменой, чтобы менять байты на месте.
const (
	phID    = "EVENTID0000000000000000000000000" // 32 символа: event_id
	phUser  = "USER00"                           // 6 цифр: user.id
	phOrder = "ORDER000"                         // 8 цифр: номер заказа в тексте
	phTime  = "9999999999.999"                   // 14 символов: секунды Unix (число, чтобы JSON был валидным)
)

func personalize(tmpl []byte, r *rand.Rand) []byte {
	b := bytes.Clone(tmpl)
	var id [16]byte
	for i := range id {
		id[i] = byte(r.UintN(256))
	}
	hexID := hex.EncodeToString(id[:])
	b = bytes.ReplaceAll(b, []byte(phID), []byte(hexID))
	b = bytes.ReplaceAll(b, []byte(phUser), fmt.Appendf(nil, "%06d", r.IntN(1_000_000)))
	b = bytes.ReplaceAll(b, []byte(phOrder), fmt.Appendf(nil, "%08d", r.IntN(100_000_000)))
	ts := float64(time.Now().UnixMilli())/1000 - r.Float64()*60
	b = bytes.ReplaceAll(b, []byte(phTime), fmt.Appendf(nil, "%014.3f", ts))
	return b
}

// sendDemo заливает историю демо-магазина — ту же, что в демо в браузере.
func sendDemo(target string) {
	envs := demo.Envelopes(time.Now())
	for _, env := range envs {
		resp, err := http.Post(target, "text/plain;charset=UTF-8", strings.NewReader(env))
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		resp.Body.Close()
		if resp.StatusCode != 200 {
			fmt.Fprintln(os.Stderr, "приём ответил", resp.Status)
			os.Exit(1)
		}
	}
	fmt.Printf("отправлено %d событий истории демо-магазина. Проблему %s... можно пометить решённой, чтобы увидеть вкладку «Решённые».\n", len(envs), demo.ResolvedTitle)
}

var reHealth = regexp.MustCompile(`queue=(\d+) written=(\d+)`)

func health(c *http.Client, base string) (written int64, queue int64) {
	resp, err := c.Get(base + "/healthz")
	if err != nil {
		return 0, -1
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	m := reHealth.FindSubmatch(b)
	if m == nil {
		return 0, -1
	}
	queue, _ = strconv.ParseInt(string(m[1]), 10, 64)
	written, _ = strconv.ParseInt(string(m[2]), 10, 64)
	return written, queue
}

var userAgents = []string{
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/128.0.0.0 Safari/537.36",
	"Mozilla/5.0 (iPhone; CPU iPhone OS 17_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Mobile/15E148 Safari/604.1",
	"Mozilla/5.0 (Linux; Android 14; Pixel 8) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/128.0.0.0 Mobile Safari/537.36",
	"Mozilla/5.0 (X11; Linux x86_64; rv:130.0) Gecko/20100101 Firefox/130.0",
}

func buildPool(n, issues int) [][]byte {
	r := rand.New(rand.NewPCG(1, 2))
	now := float64(time.Now().UnixMilli()) / 1000
	pool := make([][]byte, n)
	for i := range pool {
		issue := r.IntN(issues)
		var id [16]byte
		for j := range id {
			id[j] = byte(r.UintN(256))
		}
		frames := []map[string]any{
			{"filename": "node_modules/react-dom/cjs/react-dom.production.js", "function": "commitRoot", "lineno": 8123, "colno": 11, "in_app": false},
			{"filename": "src/app/router.ts", "function": "navigate", "lineno": 88, "in_app": true},
			{"filename": fmt.Sprintf("src/features/module%d.ts", issue%40), "function": fmt.Sprintf("handler%d", issue), "lineno": 40 + issue%30, "colno": 17, "in_app": true,
				"pre_context": []string{"export function handler(input) {", "  const items = input.items;", "  // обработка корзины"}, "context_line": "  return items.map((x) => x.price * x.qty);",
				"post_context": []string{"}", ""}},
		}
		ev := map[string]any{
			"event_id":    phID,
			"timestamp":   json.RawMessage(phTime),
			"platform":    "javascript",
			"level":       "error",
			"environment": "production",
			"release":     fmt.Sprintf("app@1.%d.0", r.IntN(3)),
			"exception": map[string]any{"values": []any{map[string]any{
				"type": fmt.Sprintf("Error%d", issue), "value": "Не удалось обработать заказ " + phOrder,
				"mechanism": map[string]any{"type": "onerror", "handled": false}, "stacktrace": map[string]any{"frames": frames},
			}}},
			"breadcrumbs": []any{
				map[string]any{"timestamp": now - 5, "category": "navigation", "message": "/catalog → /cart"},
				map[string]any{"timestamp": now - 3, "category": "fetch", "message": "GET /api/cart [200]"},
				map[string]any{"timestamp": now - 1, "category": "ui.click", "message": "button.checkout"},
			},
			"user":     map[string]any{"id": phUser},
			"request":  map[string]any{"url": "https://shop.example/cart", "headers": map[string]string{"User-Agent": userAgents[r.IntN(len(userAgents))]}},
			"tags":     map[string]string{"feature": "checkout", "region": []string{"msk", "spb", "vrn"}[r.IntN(3)]},
			"contexts": map[string]any{"trace": map[string]any{"trace_id": phID, "span_id": hex.EncodeToString(id[:8])}},
			"sdk":      map[string]any{"name": "sentry.javascript.browser", "version": "10.75.2"},
		}
		body, _ := json.Marshal(ev)
		pool[i] = append([]byte(`{"event_id":"`+ev["event_id"].(string)+`"}`+"\n"+`{"type":"event"}`+"\n"), append(body, '\n')...)
	}
	return pool
}
