//go:build js && wasm

package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"

	"github.com/Ozziess01/snag/internal/store"
	"github.com/Ozziess01/snag/internal/store/mem"
)

// История демо-магазина за две недели: чтобы у посетителя сразу были
// графики, разбивка по браузерам и все виды проблем — новые, частые,
// решённые. События идут через настоящий приём, как от SDK.

type seedIssue struct {
	count    int
	spread   time.Duration // насколько назад во времени размазаны события
	burst    int           // сколько из них за последние сутки
	platform string
	build    func(r *rand.Rand) map[string]any
}

var userAgents = []string{
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/128.0.0.0 Safari/537.36",
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 YaBrowser/24.7.0.0 Safari/537.36",
	"Mozilla/5.0 (iPhone; CPU iPhone OS 17_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Mobile/15E148 Safari/604.1",
	"Mozilla/5.0 (Linux; Android 14; Pixel 8) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/128.0.0.0 Mobile Safari/537.36",
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/128.0.0.0 Safari/537.36 Edg/128.0.2739.42",
	"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Safari/605.1.15",
	"Mozilla/5.0 (X11; Linux x86_64; rv:130.0) Gecko/20100101 Firefox/130.0",
}

func frame(file, fn string, line int, app bool, code ...string) map[string]any {
	f := map[string]any{"filename": file, "function": fn, "lineno": line, "in_app": app}
	// code — строки вокруг места падения; сама строка помечена «»».
	for i, c := range code {
		if strings.HasPrefix(c, "»") {
			f["pre_context"] = code[:i]
			f["context_line"] = strings.TrimPrefix(c, "»")
			f["post_context"] = code[i+1:]
		}
	}
	return f
}

func frontend(r *rand.Rand) map[string]any {
	return map[string]any{
		"platform": "javascript", "environment": "production",
		"release": []string{"shop@2.14.0", "shop@2.14.1"}[r.IntN(2)],
		"user":    map[string]any{"id": fmt.Sprint(1000 + r.IntN(320))},
		"request": map[string]any{
			"url":     "https://shop.example" + []string{"/cart", "/catalog/grinders", "/checkout"}[r.IntN(3)],
			"headers": map[string]string{"User-Agent": userAgents[r.IntN(len(userAgents))]},
		},
		"sdk": map[string]any{"name": "sentry.javascript.browser", "version": "10.75.2"},
	}
}

func backend(r *rand.Rand, platform, sdk string) map[string]any {
	return map[string]any{
		"platform": platform, "environment": "production",
		"release":     "api@1.9." + fmt.Sprint(3+r.IntN(2)),
		"server_name": []string{"api-1", "api-2"}[r.IntN(2)],
		"user":        map[string]any{"id": fmt.Sprint(1000 + r.IntN(320))},
		"sdk":         map[string]any{"name": sdk, "version": "demo"},
	}
}

func with(base map[string]any, extra map[string]any) map[string]any {
	for k, v := range extra {
		base[k] = v
	}
	return base
}

func exception(typ, value string, handled bool, frames ...map[string]any) map[string]any {
	return map[string]any{"values": []any{map[string]any{
		"type": typ, "value": value,
		"mechanism":  map[string]any{"type": "generic", "handled": handled},
		"stacktrace": map[string]any{"frames": frames},
	}}}
}

var seedIssues = []seedIssue{
	// Самая частая: товар пропал из каталога, пока лежал в корзине.
	{count: 64, spread: 9 * 24 * time.Hour, burst: 22, build: func(r *rand.Rand) map[string]any {
		return with(frontend(r), map[string]any{
			"exception": exception("TypeError", "Cannot read properties of undefined (reading 'price')", false,
				frame("https://shop.example/assets/vendor-3f9a1c.js", "HTMLButtonElement.<anonymous>", 2, false),
				frame("https://shop.example/assets/cart-8b2e11.js", "onCheckoutClick", 88, true,
					"export function onCheckoutClick(event) {", "  event.preventDefault();", "  const cart = store.getCart();",
					"»  const total = calcTotal(cart.items, cart.promo);", "  checkout.open({ total });", "}", "", ""),
				frame("https://shop.example/assets/cart-8b2e11.js", "calcTotal", 41, true,
					"export function calcTotal(items, promo) {", "  let sum = 0;", "  for (const id of items) {",
					"    const product = catalog.get(id);", "    // товар мог пропасть из каталога, пока лежал в корзине",
					"»    sum += product.price * qty(id);", "  }", "  return applyPromo(sum, promo);", "}"),
			),
			"breadcrumbs": []any{
				map[string]any{"category": "navigation", "message": "/catalog/grinders → /cart"},
				map[string]any{"category": "fetch", "message": "GET /api/cart [200]"},
				map[string]any{"category": "ui.click", "message": "button.checkout"},
			},
		})
	}},
	// Не догрузился кусок фронтенда после выкладки новой версии.
	{count: 18, spread: 3 * 24 * time.Hour, burst: 5, build: func(r *rand.Rand) map[string]any {
		chunk := fmt.Sprint(400 + r.IntN(90))
		return with(frontend(r), map[string]any{
			"exception": exception("ChunkLoadError", "Loading chunk "+chunk+" failed.\n(error: https://shop.example/assets/"+chunk+".js)", false,
				frame("https://shop.example/assets/runtime-1d2e3f.js", "__webpack_require__.f.j", 1, false),
				frame("https://shop.example/assets/runtime-1d2e3f.js", "Array.reduce", 1, false),
			),
		})
	}},
	// Два нажатия «Оплатить» подряд получают один номер заказа.
	{count: 12, spread: 12 * 24 * time.Hour, burst: 2, build: func(r *rand.Rand) map[string]any {
		return with(backend(r, "python", "sentry.python"), map[string]any{
			"exception": exception("UniqueViolation", `duplicate key value violates unique constraint "orders_number_key"`, false,
				frame("django/core/handlers/base.py", "_get_response", 197, false),
				frame("orders/views.py", "create", 31, true,
					"@api_view([\"POST\"])", "def create(request):", "    cart = Cart.for_user(request.user)", "    if cart.is_empty:",
					"        return Response(status=400)", "»    order = create_order(cart, request.user)", "    return Response(OrderSerializer(order).data, status=201)", "", ""),
				frame("orders/service.py", "create_order", 57, true,
					"def create_order(cart, user):", "    order = Order(user=user, total=cart.total)", "    with transaction.atomic():",
					"        order.number = next_order_number()", "        # два запроса «Оплатить» подряд получают один номер",
					"»        order.save()", "        for item in cart.items:", "            order.lines.create(product=item.product, qty=item.qty)", ""),
			),
			"transaction": "/api/orders/",
		})
	}},
	// Внешний сервис курсов валют отвечает 503.
	{count: 27, spread: 6 * 24 * time.Hour, burst: 3, build: func(r *rand.Rand) map[string]any {
		cur := []string{"USD", "EUR", "CNY"}[r.IntN(3)]
		return with(backend(r, "php", "sentry.php"), map[string]any{
			"exception": exception("RuntimeException", "Не удалось получить курс "+cur+": HTTP 503", true,
				frame("vendor/laravel/framework/src/Illuminate/Pipeline/Pipeline.php", "Illuminate\\Pipeline\\Pipeline::handle", 180, false),
				frame("app/Http/Controllers/PriceController.php", "App\\Http\\Controllers\\PriceController::show", 22, true,
					"    public function show(Product $product, Request $request): JsonResponse", "    {",
					"        $currency = $request->query('currency', 'RUB');", "»        $rate = $currency === 'RUB' ? 1.0 : $this->rates->fetch($currency);",
					"        return response()->json(['price' => round($product->price / $rate, 2)]);", "    }", "", ""),
				frame("app/Services/CurrencyRateProvider.php", "App\\Services\\CurrencyRateProvider::fetch", 34, true,
					"    public function fetch(string $currency): float", "    {",
					"        $response = $this->http->get(self::URL, ['timeout' => 2]);", "        if ($response->failed()) {",
					"»            throw new \\RuntimeException(\"Не удалось получить курс {$currency}: HTTP {$response->status()}\");",
					"        }", "        return (float) $response->json(\"rates.{$currency}\");", "    }"),
			),
		})
	}},
	// Платёжный шлюз не отвечает вовремя.
	{count: 9, spread: 2 * 24 * time.Hour, burst: 4, build: func(r *rand.Rand) map[string]any {
		return with(backend(r, "go", "sentry.go"), map[string]any{
			"exception": exception("*url.Error", `Post "https://pay.example/v1/charge": context deadline exceeded`, true,
				frame("net/http/client.go", "(*Client).do", 724, false),
				frame("shop/handlers/checkout.go", "Checkout", 41, true,
					"func Checkout(w http.ResponseWriter, r *http.Request) {", "\torder := orderFrom(r.Context())",
					"\tctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)", "\tdefer cancel()",
					"»\tpayment, err := pay.Charge(ctx, order.Total, order.Card)", "\tif err != nil {", "\t\trespondError(w, err)", "\t\treturn"),
				frame("shop/payment/client.go", "(*Client).Charge", 73, true,
					"func (c *Client) Charge(ctx context.Context, amount int64, card Card) (Payment, error) {",
					"\treq, _ := http.NewRequestWithContext(ctx, http.MethodPost, c.base+\"/v1/charge\", body(amount, card))",
					"\treq.Header.Set(\"Idempotency-Key\", idempotencyKey(ctx))", "\t// шлюз иногда отвечает дольше трёх секунд",
					"»\tresp, err := c.http.Do(req)", "\tif err != nil {", "\t\treturn Payment{}, fmt.Errorf(\"charge: %w\", err)", "\t}"),
			),
		})
	}},
	// Предупреждение без исключения: сообщение с параметром.
	{count: 40, spread: 13 * 24 * time.Hour, burst: 6, build: func(r *rand.Rand) map[string]any {
		ms := fmt.Sprint(3000 + r.IntN(4000))
		return with(backend(r, "python", "sentry.python"), map[string]any{
			"level":    "warning",
			"logger":   "payments",
			"logentry": map[string]any{"message": "Медленный ответ платёжного шлюза: %s мс", "params": []string{ms}, "formatted": "Медленный ответ платёжного шлюза: " + ms + " мс"},
		})
	}},
	// Старая проблема, которую уже решили: видна во вкладке «Решённые».
	{count: 15, spread: 13 * 24 * time.Hour, burst: 0, build: func(r *rand.Rand) map[string]any {
		return with(frontend(r), map[string]any{
			"exception": exception("SyntaxError", `Unexpected token '<', "<!DOCTYPE "... is not valid JSON`, false,
				frame("https://shop.example/assets/api-5c6d7e.js", "request", 18, true,
					"export async function request(path, init) {", "  const res = await fetch(API + path, init);",
					"  // nginx отдаёт HTML-страницу 502 вместо JSON", "  if (!res.ok) throw new HttpError(res.status);",
					"»  return res.json();", "}", "", ""),
			),
		})
	}},
}

// resolvedTitle — какую проблему пометить решённой после заливки истории.
const resolvedTitle = "SyntaxError"

func seed(h http.Handler, projectID uint64, key string, now time.Time) int {
	r := rand.New(rand.NewPCG(2026, 9))
	total := 0
	for i, is := range seedIssues {
		for n := 0; n < is.count; n++ {
			var ago time.Duration
			if n < is.burst {
				ago = time.Duration(r.Int64N(int64(24 * time.Hour)))
			} else {
				ago = 24*time.Hour + time.Duration(r.Int64N(int64(is.spread)))
			}
			if i == len(seedIssues)-1 {
				ago += 3 * 24 * time.Hour // решённая: все события старше трёх дней
			}
			ev := is.build(r)
			var id [16]byte
			for j := range id {
				id[j] = byte(r.UintN(256))
			}
			ev["event_id"] = hex.EncodeToString(id[:])
			ev["timestamp"] = float64(now.Add(-ago).UnixMilli()) / 1000
			if _, ok := ev["level"]; !ok {
				ev["level"] = "error"
			}
			send(h, projectID, key, ev)
			total++
		}
	}
	return total
}

func send(h http.Handler, projectID uint64, key string, ev map[string]any) {
	body, _ := json.Marshal(ev)
	envelope := `{"event_id":"` + ev["event_id"].(string) + `"}` + "\n" + `{"type":"event"}` + "\n" + string(body) + "\n"
	r := httptest.NewRequest("POST", fmt.Sprintf("/api/%d/envelope/?sentry_key=%s", projectID, key), strings.NewReader(envelope))
	r.RemoteAddr = "127.0.0.1:1"
	h.ServeHTTP(httptest.NewRecorder(), r)
}

func resolveSeeded(ctx context.Context, st *mem.Store, projectID uint64) {
	issues, _ := st.Issues(ctx, store.IssueFilter{ProjectID: projectID})
	for _, i := range issues {
		if strings.HasPrefix(i.Title, resolvedTitle) {
			_ = st.SetIssueStatus(ctx, i.ID, store.StatusResolved)
		}
	}
}
