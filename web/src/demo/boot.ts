// Загрузка демо: Snag в WebAssembly, SDK Sentry с транспортом прямо в него
// и интерфейс во фрейме.

import { addBreadcrumb, captureException, init, makeFetchTransport, setTag, setUser, type ErrorEvent } from '@sentry/browser';
import './demo.css';

// Код магазина в странице пользуется только этими двумя функциями SDK.
const Sentry = { addBreadcrumb, captureException };

type GoRuntime = { importObject: WebAssembly.Imports; run(instance: WebAssembly.Instance): Promise<void> };

declare global {
  interface Window {
    Go: new () => GoRuntime;
    Sentry: typeof Sentry;
  }
}

const bar = document.getElementById('boot-bar')!;
const text = document.getElementById('boot-text')!;
const status = document.getElementById('status')!;
const iframe = document.getElementById('ui') as HTMLIFrameElement;
const pageURL = location.href.split('#')[0];

let resolveReady!: () => void;
window.snagReady = new Promise((r) => (resolveReady = r));

// ---------- WebAssembly ----------

async function download(url: string, approxSize: number): Promise<Uint8Array> {
  const res = await fetch(url);
  if (!res.ok || !res.body) throw new Error(`${url}: HTTP ${res.status}`);
  const total = Number(res.headers.get('Content-Length')) || approxSize;
  const reader = res.body.getReader();
  const chunks: Uint8Array[] = [];
  let loaded = 0;
  for (;;) {
    const { done, value } = await reader.read();
    if (done) break;
    chunks.push(value);
    loaded += value.length;
    bar.style.width = `${Math.min(95, (loaded / total) * 95)}%`;
    text.textContent = `Загружаем WebAssembly… ${(loaded / 1e6).toFixed(1)} МБ`;
  }
  const out = new Uint8Array(loaded);
  let off = 0;
  for (const c of chunks) {
    out.set(c, off);
    off += c.length;
  }
  return out;
}

// Модуль лежит сжатым (snag.wasm.gz): распаковываем сами, чтобы не зависеть
// от настроек хостинга. Если сервер уже отдал его распакованным
// (Content-Encoding: gzip), просто используем как есть.
async function gunzipIfNeeded(bytes: Uint8Array): Promise<ArrayBuffer> {
  if (bytes[0] !== 0x1f || bytes[1] !== 0x8b) return bytes.buffer as ArrayBuffer;
  const stream = new Blob([bytes as BlobPart]).stream().pipeThrough(new DecompressionStream('gzip'));
  return new Response(stream).arrayBuffer();
}

async function startSnag() {
  const gz = await download('./snag.wasm.gz', 2_700_000);
  text.textContent = 'Распаковываем…';
  const wasm = await gunzipIfNeeded(gz);
  text.textContent = 'Раскладываем историю ошибок по проблемам…';
  const ready = new Promise<void>((r) => window.addEventListener('snag:ready', () => r(), { once: true }));
  const go = new window.Go();
  const { instance } = await WebAssembly.instantiate(wasm, go.importObject);
  void go.run(instance);
  await ready;
  bar.style.width = '100%';
}

// ---------- SDK Sentry ----------

let sent = 0;

// fetch для SDK: вместо сети отдаём конверт Snag внутри страницы.
async function snagFetch(input: RequestInfo | URL, init?: RequestInit): Promise<Response> {
  const url = new URL(String(input));
  const headers: Record<string, string> = {};
  new Headers(init?.headers).forEach((v, k) => (headers[k] = v));
  let body = init?.body;
  if (body instanceof Uint8Array) body = new TextDecoder().decode(body);
  const res = await window.snag!.fetch(init?.method ?? 'POST', url.pathname + url.search, headers, typeof body === 'string' ? body : undefined);
  if (typeof body === 'string' && body.includes('"type":"event"')) {
    sent++;
    const narrow = matchMedia('(max-width: 900px)').matches;
    status.textContent = `Отправлено ошибок: ${sent}. Смотрите ${narrow ? 'ниже ↓' : 'справа →'}`;
    status.classList.add('shop__status--ok');
    // На телефоне интерфейс под магазином: прокручиваем к нему.
    if (narrow && sent === 1) iframe.scrollIntoView({ behavior: 'smooth', block: 'start' });
    // Интерфейс во фрейме обновится сразу, не дожидаясь таймера.
    setTimeout(() => iframe.contentWindow?.dispatchEvent(new Event('snag:refresh')), 120);
  }
  return new Response(res.body, { status: res.status });
}

// Код магазина лежит прямо в странице. Браузер не прикладывает к стеку
// строки исходника — добавляем их сами из самой страницы, как это делает
// Sentry у себя на сервере для файлов, которые может скачать.
let pageLines: string[] = [];

function attachSource(event: ErrorEvent): ErrorEvent {
  for (const ex of event.exception?.values ?? []) {
    for (const f of ex.stacktrace?.frames ?? []) {
      // Свой код здесь — только код магазина в странице. Остальное —
      // обёртки самого SDK и бандл демо: пусть сворачиваются как библиотеки.
      f.in_app = f.filename === pageURL;
      if (!f.in_app || !f.lineno || !pageLines.length) continue;
      const i = f.lineno - 1;
      f.context_line = pageLines[i];
      f.pre_context = pageLines.slice(Math.max(0, i - 5), i);
      f.post_context = pageLines.slice(i + 1, i + 4);
    }
  }
  return event;
}

function startSentry() {
  init({
    dsn: window.snag!.dsn,
    release: 'shop@2.14.2',
    environment: 'demo',
    transport: (options) => makeFetchTransport(options, snagFetch as typeof fetch),
    // Dedupe выбрасывает одинаковые ошибки подряд, а «Сломать 25 раз»
    // как раз должна их отправить, чтобы было видно склейку.
    integrations: (defaults) => defaults.filter((i) => i.name !== 'Dedupe'),
    sendClientReports: false,
    beforeSend: attachSource,
  });
  setUser({ id: 'visitor' });
  setTag('page', 'snag-demo');
  window.Sentry = Sentry;
}

// ---------- запуск ----------

async function main() {
  try {
    const [, page] = await Promise.all([startSnag(), fetch(pageURL).then((r) => r.text()).catch(() => '')]);
    pageLines = page.split('\n');
    startSentry();
    resolveReady();
    iframe.src = `./app.html#/p/${window.snag!.project}`;
    iframe.hidden = false;
    iframe.addEventListener('load', () => document.getElementById('boot')!.remove(), { once: true });
    status.textContent = `В истории уже ${window.snag!.events} событий. Нажмите любую кнопку.`;
    window.dispatchEvent(new Event('demo:ready'));
  } catch (e) {
    text.textContent = `Не получилось запустить: ${(e as Error).message}. Нужен современный браузер с WebAssembly.`;
    status.textContent = 'Демо не запустилось.';
  }
}

main();
