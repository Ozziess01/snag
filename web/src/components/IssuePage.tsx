import { useState } from 'react';
import { api, useApi } from '../api';
import { ago, count, dateTime, num, splitTitle, STATUS_LABEL } from '../format';
import type { EventDetail, IssueDetail, PeriodName, Status } from '../types';
import EventView from './EventView';
import { Bars, ErrorBox, LevelBadge, Segmented, Spinner } from './ui';

export default function IssuePage({ issueId, eventId, onChange }: { issueId: number; eventId: string; onChange: () => void }) {
  const { data, error, loading, reload, setData } = useApi<IssueDetail>(`/api/v1/issues/${issueId}`, 15000);
  const ev = useApi<{ event: EventDetail; older: string; newer: string }>(`/api/v1/issues/${issueId}/events/${eventId}`);
  const [period, setPeriod] = useState<PeriodName>('24h');
  const [busy, setBusy] = useState(false);
  const [actionError, setActionError] = useState<string | null>(null);

  if (error) return <div className="page"><ErrorBox message={error} retry={reload} /></div>;
  if (loading || !data) return <div className="page"><Spinner /></div>;

  const { issue, project, tags } = data;
  const stats = period === '24h' ? data.stats_24h : data.stats_14d;
  const [type, text] = splitTitle(issue.title);

  const setStatus = async (status: Status) => {
    setBusy(true);
    setActionError(null);
    try {
      const r = await api<{ issue: IssueDetail['issue'] }>('PUT', `/api/v1/issues/${issue.id}`, { status });
      setData({ ...data, issue: r.issue });
      onChange();
    } catch (e) {
      setActionError((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  const bucketLabel = (i: number) =>
    dateTime(new Date(new Date(stats.period.since).getTime() + i * stats.period.step * 1000).toISOString());

  return (
    <div className="page">
      <header className="page__head issuehead">
        <div className="issuehead__main">
          <div className="crumbs">
            <a href={`#/p/${project.id}`}>{project.name}</a> <span>/</span> Проблема #{issue.id}
          </div>
          <h1 className="issuehead__title">
            {type && <b>{type}</b>}
            <span>{text}</span>
          </h1>
          <div className="issuehead__meta">
            <LevelBadge level={issue.level} />
            <span className={`badge status-${issue.status}`}>{STATUS_LABEL[issue.status]}</span>
            {issue.culprit && <code>{issue.culprit}</code>}
            <span>{issue.platform}</span>
          </div>
        </div>
        <div className="issuehead__actions">
          {issue.status === 'unresolved' ? (
            <>
              <button type="button" className="btn btn--primary" disabled={busy} onClick={() => setStatus('resolved')}>
                Решено
              </button>
              <button type="button" className="btn btn--ghost" disabled={busy} onClick={() => setStatus('ignored')}>
                Игнорировать
              </button>
            </>
          ) : (
            <button type="button" className="btn btn--ghost" disabled={busy} onClick={() => setStatus('unresolved')}>
              Открыть снова
            </button>
          )}
        </div>
      </header>
      {actionError && <ErrorBox message={actionError} />}
      {issue.status === 'resolved' && (
        <p className="note">Если ошибка случится снова, проблема откроется сама и будет помечена как «вернулась».</p>
      )}

      <div className="stats">
        <div>
          <b>{num(stats.events)}</b>
          <span>событий за {period === '24h' ? '24 часа' : '14 дней'}</span>
        </div>
        <div>
          <b>{num(stats.users)}</b>
          <span>пользователей</span>
        </div>
        <div>
          <b title={dateTime(issue.first_seen, true)}>{ago(issue.first_seen)}</b>
          <span>впервые</span>
        </div>
        <div>
          <b title={dateTime(issue.last_seen, true)}>{ago(issue.last_seen)}</b>
          <span>последний раз</span>
        </div>
        <div>
          <b>{num(issue.times_seen)}</b>
          <span>всего</span>
        </div>
      </div>

      <section className="panel">
        <div className="panel__head">
          <h2>Сколько раз случалась</h2>
          <Segmented<PeriodName>
            label="Период"
            value={period}
            onChange={setPeriod}
            options={[
              { value: '24h', label: '24 ч' },
              { value: '14d', label: '14 дн' },
            ]}
          />
        </div>
        <Bars buckets={stats.buckets} label={bucketLabel} />
      </section>

      <div className="split">
        <div className="split__main">
          {ev.error && <ErrorBox message={ev.error} retry={ev.reload} />}
          {!ev.data && !ev.error && <Spinner label="Загружаем событие…" />}
          {ev.data && <EventView issueId={issue.id} data={ev.data} />}
        </div>
        <aside className="split__side">
          <section className="panel">
            <h2>Теги</h2>
            {tags.length === 0 && <p className="muted">Тегов нет.</p>}
            {tags.map((t) => (
              <div key={t.key} className="tag">
                <div className="tag__key">{t.key}</div>
                {t.values.map((v) => {
                  const pct = t.total ? Math.round((v.count / t.total) * 100) : 0;
                  return (
                    <div key={v.value} className="tag__row" title={count(v.count, 'событие', 'события', 'событий')}>
                      <span className="tag__bar" style={{ width: `${pct}%` }} />
                      <span className="tag__value">{v.value}</span>
                      <span className="tag__pct">{pct}%</span>
                    </div>
                  );
                })}
              </div>
            ))}
          </section>
        </aside>
      </div>
    </div>
  );
}
