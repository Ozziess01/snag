import * as Sentry from '@sentry/node';

Sentry.init({
  dsn: process.env.SENTRY_DSN || 'http://node@127.0.0.1:9911/1',
  release: 'demo@1.2.0',
  environment: 'test',
  sendDefaultPii: true,
  tracesSampleRate: 0,
});

function parseOrder(raw) {
  return JSON.parse(raw).items.map((i) => i.price);
}
function checkout() {
  return parseOrder('{"items": null}');
}

Sentry.setUser({ id: 42, email: 'user@example.com' });
Sentry.setTag('feature', 'checkout');
Sentry.addBreadcrumb({ category: 'ui', message: 'нажал «Оплатить»' });

// 1. Обычное исключение из вложенных функций.
try {
  checkout();
} catch (e) {
  Sentry.captureException(e);
}

// 2. Сообщение без исключения.
Sentry.captureMessage('Order 17 failed', 'warning');

// 3. Цепочка причин (Error.cause).
try {
  try {
    JSON.parse('{oops');
  } catch (cause) {
    throw new Error('не удалось прочитать конфиг', { cause });
  }
} catch (e) {
  Sentry.captureException(e);
}

// 4. Большое событие: Node SDK сжимает тела больше 32 КБ.
Sentry.withScope((scope) => {
  scope.setExtra('payload', 'x'.repeat(60000));
  Sentry.captureException(new RangeError('слишком большой заказ'));
});

await Sentry.flush(5000);
