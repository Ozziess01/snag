import { useEffect, useState } from 'react';
import { useApi } from '../api';
import { ago, count, dateTime, num, splitTitle, STATUS_LABEL } from '../format';
import { navigate } from '../router';
import type { IssueRow, Period, PeriodName, Project } from '../types';
import { Empty, ErrorBox, Segmented, Sparkline, Spinner } from './ui';

type StatusFilter = 'unresolved' | 'resolved' | 'ignored' | 'all';
type Sort = 'last' | 'events' | 'users' | 'new';

const DAY = 24 * 3600 * 1000;

export default function IssuesPage({ projectId, query, projects }: { projectId: number; query: URLSearchParams; projects: Project[] | null }) {
  const status = (query.get('status') as StatusFilter) || 'unresolved';
  const period = (query.get('period') as PeriodName) || '24h';
  const sort = (query.get('sort') as Sort) || 'last';
  const q = query.get('q') ?? '';
  const [search, setSearch] = useState(q);

  const set = (patch: Record<string, string>) => {
    const next = new URLSearchParams(query);
    for (const [k, v] of Object.entries(patch)) {
      if (v) next.set(k, v);
      else next.delete(k);
    }
    const s = next.toString();
    navigate(`/p/${projectId}${s ? '?' + s : ''}`, true);
  };

  // Поиск применяем с небольшой задержкой, а не на каждую букву.
  useEffect(() => {
    const t = setTimeout(() => {
      if (search !== q) set({ q: search });
    }, 300);
    return () => clearTimeout(t);
  }, [search]);

  const path = `/api/v1/projects/${projectId}/issues?status=${status}&period=${period}&sort=${sort}${q ? '&q=' + encodeURIComponent(q) : ''}`;
  const { data, error, loading, reload } = useApi<{ period: Period; issues: IssueRow[] | null }>(path, 5000);
  const project = projects?.find((p) => p.id === projectId);
  const issues = data?.issues ?? [];

  const bucketLabel = (i: number) => {
    if (!data) return '';
    const start = new Date(new Date(data.period.since).getTime() + i * data.period.step * 1000).toISOString();
    return dateTime(start);
  };

  return (
    <div className="page">
      <header className="page__head">
        <div>
          <div className="crumbs">Проблемы</div>
          <h1>{project?.name ?? 'Проект'}</h1>
        </div>
        <div className="page__actions">
          <a className="btn btn--ghost" href={`#/p/${projectId}/alerts`}>
            Уведомления
          </a>
          <a className="btn btn--ghost" href={`#/p/${projectId}/setup`}>
            Подключение
          </a>
        </div>
      </header>

      <div className="toolbar">
        <Segmented<StatusFilter>
          label="Статус"
          value={status}
          onChange={(v) => set({ status: v === 'unresolved' ? '' : v })}
          options={[
            { value: 'unresolved', label: 'Открытые' },
            { value: 'resolved', label: 'Решённые' },
            { value: 'ignored', label: 'Игнорируемые' },
            { value: 'all', label: 'Все' },
          ]}
        />
        <input
          className="search"
          type="search"
          placeholder="Поиск по тексту ошибки или файлу"
          value={search}
          onChange={(e) => setSearch(e.target.value)}
          aria-label="Поиск"
        />
        <div className="toolbar__right">
          <select value={sort} onChange={(e) => set({ sort: e.target.value === 'last' ? '' : e.target.value })} aria-label="Сортировка">
            <option value="last">Последние</option>
            <option value="events">Частые</option>
            <option value="users">Больше пользователей</option>
            <option value="new">Новые</option>
          </select>
          <Segmented<PeriodName>
            label="Период"
            value={period}
            onChange={(v) => set({ period: v === '24h' ? '' : v })}
            options={[
              { value: '24h', label: '24 ч' },
              { value: '14d', label: '14 дн' },
            ]}
          />
        </div>
      </div>

      {error && <ErrorBox message={error} retry={reload} />}
      {loading && !data && <Spinner />}

      {data && issues.length === 0 && (
        <Empty title={q ? 'Ничего не нашлось' : status === 'unresolved' ? 'Открытых проблем нет' : 'Здесь пусто'}>
          {status === 'unresolved' && !q && (
            <>
              Как только приложение упадёт, ошибка появится здесь. Ещё не подключили SDK?{' '}
              <a href={`#/p/${projectId}/setup`}>Инструкция и тестовая ошибка</a>
            </>
          )}
        </Empty>
      )}

      {issues.length > 0 && (
        <div className="issues" role="table" aria-label="Проблемы">
          <div className="issues__head" role="row">
            <span role="columnheader">Проблема</span>
            <span role="columnheader">{period === '24h' ? 'За 24 часа' : 'За 14 дней'}</span>
            <span role="columnheader" className="num">События</span>
            <span role="columnheader" className="num">Польз.</span>
          </div>
          {issues.map((i) => {
            const [type, text] = splitTitle(i.title);
            const isNew = Date.now() - new Date(i.first_seen).getTime() < DAY;
            const regressed = i.regressed_at && Date.now() - new Date(i.regressed_at).getTime() < 7 * DAY;
            return (
              <a key={i.id} className="issue" role="row" href={`#/issues/${i.id}`}>
                <span className={`issue__level level-${i.level}`} aria-hidden="true" />
                <span className="issue__main" role="cell">
                  <span className="issue__title">
                    {type && <b>{type}</b>}
                    <span>{text}</span>
                  </span>
                  <span className="issue__meta">
                    {i.culprit && <code>{i.culprit}</code>}
                    <span>{i.platform}</span>
                    <span title={dateTime(i.last_seen, true)}>{ago(i.last_seen)}</span>
                    <span title={dateTime(i.first_seen, true)}>впервые {ago(i.first_seen)}</span>
                    {isNew && <span className="badge badge--new">новая</span>}
                    {regressed && <span className="badge badge--regressed">вернулась</span>}
                    {status === 'all' && i.status !== 'unresolved' && <span className="badge">{STATUS_LABEL[i.status]}</span>}
                  </span>
                </span>
                <span role="cell" className="issue__graph">
                  <Sparkline buckets={i.buckets} title={(b) => `${bucketLabel(b)}: ${num(i.buckets[b])}`} />
                </span>
                <span role="cell" className="num" title={count(i.times_seen, 'событие', 'события', 'событий') + ' за всё время'}>
                  {num(i.events)}
                </span>
                <span role="cell" className="num">{num(i.users)}</span>
              </a>
            );
          })}
        </div>
      )}
    </div>
  );
}
