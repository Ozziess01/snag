import { useState } from 'react';
import { dateTime, shortId, time } from '../format';
import type { Breadcrumb, EventDetail, ExceptionInfo, Frame } from '../types';
import { CopyButton, LevelBadge } from './ui';

export default function EventView({ issueId, data }: { issueId: number; data: { event: EventDetail; older: string; newer: string } }) {
  const { event: e, older, newer } = data;
  // Главное исключение — последнее в цепочке: показываем его первым.
  const exceptions = [...(e.exceptions ?? [])].reverse();

  return (
    <div className="event">
      <div className="eventnav">
        <div>
          <span className="muted">Событие</span> <code>{shortId(e.id)}</code>{' '}
          <span className="muted" title={`получено ${dateTime(e.received_at, true)}`}>{dateTime(e.timestamp, true)}</span>
        </div>
        <div className="eventnav__btns">
          <a className={`btn btn--ghost btn--sm ${older ? '' : 'disabled'}`} href={older ? `#/issues/${issueId}/events/${older}` : undefined} aria-disabled={!older}>
            ← Старее
          </a>
          <a className={`btn btn--ghost btn--sm ${newer ? '' : 'disabled'}`} href={newer ? `#/issues/${issueId}/events/${newer}` : undefined} aria-disabled={!newer}>
            Новее →
          </a>
          <a className="btn btn--ghost btn--sm" href={`#/issues/${issueId}`}>
            Последнее
          </a>
        </div>
      </div>

      <div className="eventmeta">
        <LevelBadge level={e.level} />
        {e.environment && <span>окружение <b>{e.environment}</b></span>}
        {e.release && <span>версия <b>{e.release}</b></span>}
        {e.sdk && <span>{e.sdk.name} {e.sdk.version}</span>}
      </div>

      {exceptions.map((x, i) => (
        <section key={i} className="panel">
          {i > 0 && <div className="chain">Причина предыдущей ошибки</div>}
          <ExceptionBlock x={x} />
        </section>
      ))}

      {exceptions.length === 0 && (
        <section className="panel">
          <h2>Сообщение</h2>
          <p className="message">{e.message || e.title}</p>
          {e.stacktrace && e.stacktrace.length > 0 && <Stacktrace frames={e.stacktrace} />}
        </section>
      )}

      {e.breadcrumbs && e.breadcrumbs.length > 0 && <Breadcrumbs items={e.breadcrumbs} eventTime={e.timestamp} />}

      {e.request && (e.request.url || e.request.headers) && (
        <section className="panel">
          <h2>Запрос</h2>
          <p className="request__line">
            {e.request.method && <b>{e.request.method}</b>} <code>{e.request.url}</code>
          </p>
          {e.request.headers && <KV rows={e.request.headers} />}
          {e.request.data != null && <Json value={e.request.data} />}
        </section>
      )}

      {e.user && (
        <section className="panel">
          <h2>Пользователь</h2>
          <KV rows={Object.entries(e.user).filter(([, v]) => v).map(([k, v]) => [k, String(v)])} />
        </section>
      )}

      {e.contexts && Object.keys(e.contexts).length > 0 && (
        <section className="panel">
          <h2>Окружение</h2>
          <div className="contexts">
            {Object.entries(e.contexts).map(([name, ctx]) => (
              <div key={name} className="context">
                <div className="context__name">{name}</div>
                <KV rows={Object.entries(ctx ?? {}).filter(([k]) => k !== 'type').map(([k, v]) => [k, fmt(v)])} />
              </div>
            ))}
          </div>
        </section>
      )}

      {e.tags && e.tags.length > 0 && (
        <section className="panel">
          <h2>Теги события</h2>
          <div className="chips">
            {e.tags.map(([k, v]) => (
              <span key={k} className="chip">
                <span>{k}</span>
                {v}
              </span>
            ))}
          </div>
        </section>
      )}

      {e.extra && Object.keys(e.extra).length > 0 && (
        <section className="panel">
          <h2>Дополнительно</h2>
          <KV rows={Object.entries(e.extra).map(([k, v]) => [k, fmt(v)])} />
        </section>
      )}

      <RawJson value={e.raw} />
    </div>
  );
}

function ExceptionBlock({ x }: { x: ExceptionInfo }) {
  const unhandled = x.mechanism?.handled === false;
  return (
    <div className="exception">
      <h2 className="exception__title">
        <b>{x.type || 'Ошибка'}</b>
        {unhandled && <span className="badge badge--unhandled">не перехвачена</span>}
      </h2>
      {x.value && <p className="exception__value">{x.value}</p>}
      {x.frames && x.frames.length > 0 ? <Stacktrace frames={x.frames} /> : <p className="muted">Стектрейса нет.</p>}
    </div>
  );
}

// Stacktrace: сверху место падения. Кадры библиотек (in_app = false)
// свёрнуты в группы, свой код раскрыт — как в Sentry.
function Stacktrace({ frames }: { frames: Frame[] }) {
  const ordered = [...frames].reverse();
  const hasApp = ordered.some((f) => f.in_app);
  const groups: { app: boolean; frames: Frame[] }[] = [];
  for (const f of ordered) {
    const app = !hasApp || f.in_app;
    const last = groups[groups.length - 1];
    if (last && last.app === app) last.frames.push(f);
    else groups.push({ app, frames: [f] });
  }
  const firstApp = ordered.find((f) => !hasApp || f.in_app);

  return (
    <ol className="frames">
      {groups.map((g, gi) =>
        g.app ? (
          g.frames.map((f, fi) => <FrameRow key={`${gi}-${fi}`} f={f} open={f === firstApp} />)
        ) : (
          <SystemFrames key={gi} frames={g.frames} />
        ),
      )}
    </ol>
  );
}

function SystemFrames({ frames }: { frames: Frame[] }) {
  const [open, setOpen] = useState(false);
  if (open) return <>{frames.map((f, i) => <FrameRow key={i} f={f} open={false} />)}</>;
  return (
    <li className="frames__more">
      <button type="button" onClick={() => setOpen(true)}>
        Показать {frames.length} {frames.length === 1 ? 'кадр' : frames.length < 5 ? 'кадра' : 'кадров'} библиотек
      </button>
    </li>
  );
}

function FrameRow({ f, open: initial }: { f: Frame; open: boolean }) {
  const [open, setOpen] = useState(initial);
  const full = f.filename || f.abs_path || f.module || '<неизвестно>';
  const path = displayPath(full);
  const slash = Math.max(path.lastIndexOf('/'), path.lastIndexOf('\\'));
  const dir = slash >= 0 ? path.slice(0, slash + 1) : '';
  const file = path.slice(slash + 1);
  const canOpen = !!f.context?.length;

  return (
    <li className={`frame ${f.in_app ? 'frame--app' : ''}`}>
      <button type="button" className="frame__head" onClick={() => canOpen && setOpen(!open)} aria-expanded={canOpen ? open : undefined} disabled={!canOpen}>
        <span className="frame__path" title={full}>
          <span className="muted">{dir}</span>
          <b>{file}</b>
        </span>
        {f.function && (
          <span className="frame__fn">
            <span className="muted"> в </span>
            <code>{f.function}</code>
          </span>
        )}
        {f.lineno > 0 && (
          <span className="frame__line">
            <span className="muted"> строка </span>
            {f.lineno}
            {f.colno > 0 && <span className="muted">:{f.colno}</span>}
          </span>
        )}
        {canOpen && <span className="frame__toggle" aria-hidden="true">{open ? '−' : '+'}</span>}
      </button>
      {open && f.context && (
        <pre className="code">
          {f.context.map((l) => (
            <div key={l.line} className={l.line === f.lineno ? 'code__hit' : ''}>
              <span className="code__no">{l.line}</span>
              {l.code || ' '}
            </div>
          ))}
        </pre>
      )}
    </li>
  );
}

// displayPath: у браузерных кадров полный URL с параметрами — показываем путь.
function displayPath(p: string): string {
  if (!p.includes('://')) return p;
  try {
    return new URL(p).pathname || p;
  } catch {
    return p;
  }
}

function Breadcrumbs({ items, eventTime }: { items: Breadcrumb[]; eventTime: string }) {
  const [all, setAll] = useState(false);
  const list = [...items].reverse();
  const shown = all ? list : list.slice(0, 10);
  const t0 = new Date(eventTime).getTime();
  return (
    <section className="panel">
      <h2>Что происходило перед ошибкой</h2>
      <ol className="crumbs-list">
        {shown.map((b, i) => (
          <li key={i} className={`crumb level-${b.level || 'info'}`}>
            <span className="crumb__time" title={b.timestamp ? dateTime(b.timestamp, true) : ''}>
              {b.timestamp ? `−${Math.max(0, (t0 - new Date(b.timestamp).getTime()) / 1000).toFixed(1)} с` : ''}
            </span>
            <span className="crumb__cat">{b.category || b.type || 'default'}</span>
            <span className="crumb__msg">
              {b.message}
              {b.data && Object.keys(b.data).length > 0 && <code className="crumb__data">{fmt(b.data)}</code>}
            </span>
            <span className="crumb__abs muted">{b.timestamp ? time(b.timestamp) : ''}</span>
          </li>
        ))}
      </ol>
      {list.length > 10 && !all && (
        <button type="button" className="btn btn--ghost btn--sm" onClick={() => setAll(true)}>
          Показать все ({list.length})
        </button>
      )}
    </section>
  );
}

function KV({ rows }: { rows: [string, string][] }) {
  if (!rows.length) return null;
  return (
    <dl className="kv">
      {rows.map(([k, v], i) => (
        <div key={i}>
          <dt>{k}</dt>
          <dd>{v}</dd>
        </div>
      ))}
    </dl>
  );
}

function Json({ value }: { value: unknown }) {
  return <pre className="json">{typeof value === 'string' ? value : JSON.stringify(value, null, 2)}</pre>;
}

function RawJson({ value }: { value: unknown }) {
  const [open, setOpen] = useState(false);
  if (value == null) return null;
  const text = JSON.stringify(value, null, 2);
  return (
    <section className="panel">
      <div className="panel__head">
        <h2>Исходный JSON</h2>
        <div className="panel__actions">
          {open && <CopyButton text={text} />}
          <button type="button" className="btn btn--ghost btn--sm" onClick={() => setOpen(!open)}>
            {open ? 'Скрыть' : 'Показать'}
          </button>
        </div>
      </div>
      {open && <pre className="json">{text}</pre>}
    </section>
  );
}

function fmt(v: unknown): string {
  if (v == null) return '';
  if (typeof v === 'string') return v;
  return JSON.stringify(v);
}
