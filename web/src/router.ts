// Навигация по hash (#/issues/5): так интерфейс работает и из бинарника,
// и со статического хостинга демо без настройки сервера.

import { useEffect, useState } from 'react';

export type Route =
  | { name: 'login' }
  | { name: 'home' }
  | { name: 'issues'; projectId: number; query: URLSearchParams }
  | { name: 'setup'; projectId: number }
  | { name: 'alerts'; projectId: number }
  | { name: 'newProject' }
  | { name: 'issue'; issueId: number; eventId: string };

export function parse(hash: string): Route {
  const [path, qs = ''] = hash.replace(/^#/, '').split('?');
  const parts = path.split('/').filter(Boolean);
  const query = new URLSearchParams(qs);
  const num = (s: string | undefined) => (s && /^\d+$/.test(s) ? Number(s) : 0);

  if (parts[0] === 'login') return { name: 'login' };
  if (parts[0] === 'projects' && parts[1] === 'new') return { name: 'newProject' };
  if (parts[0] === 'p' && num(parts[1])) {
    if (parts[2] === 'setup') return { name: 'setup', projectId: num(parts[1]) };
    if (parts[2] === 'alerts') return { name: 'alerts', projectId: num(parts[1]) };
    return { name: 'issues', projectId: num(parts[1]), query };
  }
  if (parts[0] === 'issues' && num(parts[1])) {
    return { name: 'issue', issueId: num(parts[1]), eventId: parts[2] === 'events' && parts[3] ? parts[3] : 'latest' };
  }
  return { name: 'home' };
}

export function navigate(to: string, replace = false) {
  const hash = to.startsWith('#') ? to : '#' + to;
  if (replace) {
    history.replaceState(null, '', hash);
    window.dispatchEvent(new HashChangeEvent('hashchange'));
  } else {
    location.hash = hash;
  }
}

export function useRoute(): Route {
  const [route, setRoute] = useState(() => parse(location.hash));
  useEffect(() => {
    const on = () => setRoute(parse(location.hash));
    window.addEventListener('hashchange', on);
    return () => window.removeEventListener('hashchange', on);
  }, []);
  return route;
}
