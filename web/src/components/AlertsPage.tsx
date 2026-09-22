import { useState } from 'react';
import { api, useApi } from '../api';
import type { Channel, Project } from '../types';
import { ErrorBox, Spinner } from './ui';

type Settings = { telegram: { enabled: boolean; bot: string } };

export default function AlertsPage({ projectId, projects, demo }: { projectId: number; projects: Project[] | null; demo: boolean }) {
  const settings = useApi<Settings>('/api/v1/settings');
  const list = useApi<{ channels: Channel[] }>(`/api/v1/projects/${projectId}/channels`);
  const project = projects?.find((p) => p.id === projectId);
  const tg = settings.data?.telegram;

  return (
    <div className="page page--narrow">
      <header className="page__head">
        <div>
          <div className="crumbs">
            <a href={`#/p/${projectId}`}>{project?.name ?? 'Проект'}</a> <span>/</span> Уведомления
          </div>
          <h1>Уведомления в Telegram</h1>
        </div>
      </header>

      {tg && !tg.enabled && (
        <div className="notice">
          {demo ? (
            <>В демо сообщения не отправляются: у него нет сервера. Настройки ниже работают, но кнопка «Проверить» ответит ошибкой.</>
          ) : (
            <>
              Бот не настроен на сервере. Создайте бота у <a href="https://t.me/BotFather" target="_blank" rel="noopener">@BotFather</a> и
              запустите snag с переменной <code>SNAG_TELEGRAM_TOKEN</code>.
            </>
          )}
        </div>
      )}

      <section className="panel">
        <h2>Как подключить чат</h2>
        <ol className="steps">
          <li>
            {tg?.bot ? (
              <>Напишите боту <a href={`https://t.me/${tg.bot}`} target="_blank" rel="noopener">@{tg.bot}</a> команду <code>/start</code>.</>
            ) : (
              <>Напишите боту Snag команду <code>/start</code>.</>
            )}{' '}
            Для группы добавьте бота в неё и отправьте <code>/chatid</code>.
          </li>
          <li>Бот ответит номером чата. Вставьте его ниже и нажмите «Проверить».</li>
          <li>Для публичного канала вместо номера подойдёт его имя: <code>@my_channel</code> (бот должен быть админом канала).</li>
        </ol>
      </section>

      <AddForm projectId={projectId} onAdded={list.reload} />

      {list.error && <ErrorBox message={list.error} retry={list.reload} />}
      {!list.data && !list.error && <Spinner />}
      {list.data && list.data.channels.length > 0 && (
        <section className="panel">
          <h2>Подключённые чаты</h2>
          <div className="channels">
            {list.data.channels.map((c) => (
              <ChannelRow key={c.id} projectId={projectId} c={c} onChange={list.reload} />
            ))}
          </div>
        </section>
      )}
    </div>
  );
}

function AddForm({ projectId, onAdded }: { projectId: number; onAdded: () => void }) {
  const [target, setTarget] = useState('');
  const [onNew, setOnNew] = useState(true);
  const [onRegression, setOnRegression] = useState(true);
  const [spike, setSpike] = useState('50');
  const [spikeOn, setSpikeOn] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  return (
    <form
      className="panel form"
      onSubmit={async (e) => {
        e.preventDefault();
        setBusy(true);
        setError(null);
        try {
          await api('POST', `/api/v1/projects/${projectId}/channels`, {
            target: target.trim(),
            on_new: onNew,
            on_regression: onRegression,
            spike_threshold: spikeOn ? Number(spike) || 0 : 0,
            spike_window: 5,
          });
          setTarget('');
          onAdded();
        } catch (err) {
          setError((err as Error).message);
        } finally {
          setBusy(false);
        }
      }}
    >
      <h2>Добавить чат</h2>
      <label>
        Номер чата или @канал
        <input required value={target} onChange={(e) => setTarget(e.target.value)} placeholder="-1001234567890" inputMode="text" />
      </label>
      <div className="checks">
        <label className="check">
          <input type="checkbox" checked={onNew} onChange={(e) => setOnNew(e.target.checked)} /> Новые проблемы
        </label>
        <label className="check">
          <input type="checkbox" checked={onRegression} onChange={(e) => setOnRegression(e.target.checked)} /> Проблема вернулась после «Решено»
        </label>
        <label className="check">
          <input type="checkbox" checked={spikeOn} onChange={(e) => setSpikeOn(e.target.checked)} /> Всплеск: от
          <input type="number" min={1} className="num-input" value={spike} onChange={(e) => setSpike(e.target.value)} disabled={!spikeOn} aria-label="Порог" />
          событий за 5 минут
        </label>
      </div>
      {error && <div className="login__error" role="alert">{error}</div>}
      <button type="submit" className="btn btn--primary" disabled={busy}>
        Добавить
      </button>
    </form>
  );
}

function ChannelRow({ projectId, c, onChange }: { projectId: number; c: Channel; onChange: () => void }) {
  const [status, setStatus] = useState<string | null>(null);
  const base = `/api/v1/projects/${projectId}/channels/${c.id}`;

  const update = async (patch: Partial<Channel>) => {
    try {
      await api('PUT', base, { ...c, ...patch });
      onChange();
    } catch (e) {
      setStatus((e as Error).message);
    }
  };

  return (
    <div className="channel">
      <div className="channel__main">
        <code className="channel__target">{c.target}</code>
        <div className="channel__opts">
          <label className="check">
            <input type="checkbox" checked={c.on_new} onChange={(e) => update({ on_new: e.target.checked })} /> новые
          </label>
          <label className="check">
            <input type="checkbox" checked={c.on_regression} onChange={(e) => update({ on_regression: e.target.checked })} /> вернувшиеся
          </label>
          <span className="muted">{c.spike_threshold > 0 ? `всплеск от ${c.spike_threshold} за ${c.spike_window} мин` : 'всплески выкл.'}</span>
        </div>
        {status && <div className={status === 'Сообщение отправлено' ? 'ok' : 'login__error'}>{status}</div>}
      </div>
      <div className="channel__btns">
        <button
          type="button"
          className="btn btn--ghost btn--sm"
          onClick={async () => {
            setStatus(null);
            try {
              await api('POST', `${base}/test`);
              setStatus('Сообщение отправлено');
            } catch (e) {
              setStatus((e as Error).message);
            }
          }}
        >
          Проверить
        </button>
        <button
          type="button"
          className="btn btn--ghost btn--sm"
          onClick={async () => {
            if (!confirm(`Отключить уведомления в ${c.target}?`)) return;
            await api('DELETE', base).catch((e) => setStatus((e as Error).message));
            onChange();
          }}
        >
          Удалить
        </button>
      </div>
    </div>
  );
}
