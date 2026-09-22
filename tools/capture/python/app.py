import sentry_sdk

sentry_sdk.init(
    dsn="http://python@127.0.0.1:9911/1",
    release="demo@1.2.0",
    environment="test",
    send_default_pii=True,
)


def parse_quantity(raw):
    return int(raw)


def handler():
    return parse_quantity("три")


sentry_sdk.set_user({"id": 42, "email": "user@example.com"})
sentry_sdk.set_tag("feature", "checkout")
sentry_sdk.add_breadcrumb(category="ui", message="нажал «Оплатить»")

# 1. Обычное исключение.
try:
    handler()
except Exception as e:
    sentry_sdk.capture_exception(e)

# 2. Сообщение.
sentry_sdk.capture_message("Order 17 failed", level="warning")

# 3. Цепочка: raise ... from ...
try:
    try:
        {}["missing"]
    except KeyError as e:
        raise RuntimeError("не нашли настройку") from e
except Exception as e:
    sentry_sdk.capture_exception(e)

sentry_sdk.flush(5)
