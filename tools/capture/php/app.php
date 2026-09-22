<?php

require __DIR__ . '/vendor/autoload.php';

\Sentry\init([
    'dsn' => 'http://php@127.0.0.1:9911/1',
    'release' => 'demo@1.2.0',
    'environment' => 'test',
    'send_default_pii' => true,
]);

function findUser(int $id): array
{
    throw new \RuntimeException("User {$id} not found");
}

function showProfile(): array
{
    return findUser(7);
}

\Sentry\configureScope(function (\Sentry\State\Scope $scope): void {
    $scope->setUser(['id' => 42, 'email' => 'user@example.com']);
    $scope->setTag('feature', 'checkout');
});
\Sentry\addBreadcrumb(new \Sentry\Breadcrumb(\Sentry\Breadcrumb::LEVEL_INFO, \Sentry\Breadcrumb::TYPE_USER, 'ui', 'нажал «Оплатить»'));

// 1. Исключение из вложенных функций.
try {
    showProfile();
} catch (\Throwable $e) {
    \Sentry\captureException($e);
}

// 2. Сообщение.
\Sentry\captureMessage('Order 17 failed', \Sentry\Severity::warning());

// 3. Цепочка через previous.
try {
    intdiv(1, 0);
} catch (\Throwable $e) {
    \Sentry\captureException(new \LogicException('не удалось посчитать скидку', 0, $e));
}

\Sentry\flush();
