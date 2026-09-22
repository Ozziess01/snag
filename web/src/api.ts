// Запросы к API. Транспорт подменяемый: на сервере это обычный fetch,
// в демо — вызов Snag, собранного в WebAssembly прямо в этой вкладке
// (window.snag.fetch). Интерфейс об этом не знает.

import { useCallback, useEffect, useRef, useState } from 'react';

export interface RawResponse {
  status: number;
  body: string;
}

export type Transport = (method: string, url: string, headers: Record<string, string>, body?: string) => Promise<RawResponse>;

const fetchTransport: Transport = async (method, url, headers, body) => {
  const res = await fetch(url, { method, headers, body, credentials: 'same-origin' });
  return { status: res.status, body: await res.text() };
};

declare global {
  interface Window {
    snag?: { fetch: Transport; demo: true; dsn: string; project: number; events: number };
    snagReady?: Promise<void>;
  }
}

// В демо интерфейс открыт во фрейме, а Snag (WebAssembly) живёт на
// родительской странице: берём его оттуда. Чужой родитель (интерфейс
// встроили на другой сайт) бросит исключение — тогда обычный fetch.
function host(): Window | null {
  if (window.snag) return window;
  try {
    if (window.parent !== window && window.parent.snag) return window.parent;
  } catch {
    // другой origin
  }
  return null;
}

// isEmbeddedDemo: интерфейс открыт во фрейме демо-страницы.
export function isEmbeddedDemo(): boolean {
  const h = host();
  return h !== null && h !== window;
}

function transport(): Transport {
  return host()?.snag?.fetch ?? fetchTransport;
}

// waitForDemo: в демо дожидаемся, пока WebAssembly загрузится и
// разложит историю по проблемам.
export async function waitForDemo(): Promise<void> {
  try {
    const ready = window.snagReady ?? (window.parent !== window ? window.parent.snagReady : undefined);
    if (ready) await ready;
  } catch {
    // другой origin
  }
}

export class ApiError extends Error {
  constructor(public status: number, message: string) {
    super(message);
  }
}

let onUnauthorized: () => void = () => {};
export function setUnauthorizedHandler(fn: () => void) {
  onUnauthorized = fn;
}

export async function api<T>(method: string, path: string, body?: unknown): Promise<T> {
  const headers: Record<string, string> = { Accept: 'application/json' };
  if (method !== 'GET') {
    headers['X-Requested-With'] = 'snag';
    headers['Content-Type'] = 'application/json';
  }
  let res: RawResponse;
  try {
    res = await transport()(method, path, headers, body === undefined ? undefined : JSON.stringify(body));
  } catch {
    throw new ApiError(0, 'Сервер недоступен. Проверьте, что snag запущен.');
  }
  const data = res.body ? safeJSON(res.body) : null;
  if (res.status === 401 && !path.endsWith('/auth/login')) {
    onUnauthorized();
  }
  if (res.status >= 400) {
    const msg = (data as { error?: string } | null)?.error ?? `Ошибка ${res.status}`;
    throw new ApiError(res.status, msg);
  }
  return data as T;
}

function safeJSON(s: string): unknown {
  try {
    return JSON.parse(s);
  } catch {
    return null;
  }
}

// sendRaw отправляет конверт на приём так же, как это делает SDK.
export async function sendEnvelope(projectId: number, key: string, envelope: string): Promise<number> {
  const res = await transport()(
    'POST',
    `/api/${projectId}/envelope/?sentry_key=${key}&sentry_version=7&sentry_client=snag-ui/1.0`,
    { 'Content-Type': 'text/plain;charset=UTF-8' },
    envelope,
  );
  return res.status;
}

// useApi — загрузка данных с перезапросом по reload() и, если задано,
// по таймеру (пока вкладка видна).
export function useApi<T>(path: string | null, refreshMs = 0) {
  const [data, setData] = useState<T | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(path !== null);
  const seq = useRef(0);

  const load = useCallback(
    async (quiet = false) => {
      if (path === null) return;
      const my = ++seq.current;
      if (!quiet) setLoading(true);
      try {
        const d = await api<T>('GET', path);
        if (my === seq.current) {
          setData(d);
          setError(null);
        }
      } catch (e) {
        if (my === seq.current) setError((e as Error).message);
      } finally {
        if (my === seq.current) setLoading(false);
      }
    },
    [path],
  );

  useEffect(() => {
    setData(null);
    load();
  }, [load]);

  // Демо-страница шлёт snag:refresh, как только отправила ошибку:
  // список обновляется сразу, не дожидаясь таймера.
  useEffect(() => {
    const on = () => load(true);
    window.addEventListener('snag:refresh', on);
    return () => window.removeEventListener('snag:refresh', on);
  }, [load]);

  useEffect(() => {
    if (!refreshMs) return;
    const t = setInterval(() => {
      if (document.visibilityState === 'visible') load(true);
    }, refreshMs);
    return () => clearInterval(t);
  }, [load, refreshMs]);

  return { data, error, loading, reload: () => load(true), setData };
}
