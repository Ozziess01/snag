import { useState } from 'react';
import { sendEnvelope } from '../api';
import { navigate } from '../router';
import type { Project } from '../types';
import { CopyButton, Segmented, Spinner } from './ui';

type Lang = 'browser' | 'node' | 'python' | 'php' | 'go';

const snippets: Record<Lang, { label: string; install: string; code: (dsn: string) => string }> = {
  browser: {
    label: 'JavaScript',
    install: 'npm install @sentry/browser',
    code: (dsn) => `import * as Sentry from '@sentry/browser';

Sentry.init({
  dsn: '${dsn}',
  release: 'my-site@1.0.0',
  environment: 'production',
});`,
  },
  node: {
    label: 'Node.js',
    install: 'npm install @sentry/node',
    code: (dsn) => `import * as Sentry from '@sentry/node';

Sentry.init({
  dsn: '${dsn}',
  environment: 'production',
});`,
  },
  python: {
    label: 'Python',
    install: 'pip install sentry-sdk',
    code: (dsn) => `import sentry_sdk

sentry_sdk.init(
    dsn="${dsn}",
    environment="production",
)`,
  },
  php: {
    label: 'PHP',
    install: 'composer require sentry/sentry',
    code: (dsn) => `\\Sentry\\init([
    'dsn' => '${dsn}',
    'environment' => 'production',
]);`,
  },
  go: {
    label: 'Go',
    install: 'go get github.com/getsentry/sentry-go',
    code: (dsn) => `err := sentry.Init(sentry.ClientOptions{
    Dsn:         "${dsn}",
    Environment: "production",
})
defer sentry.Flush(2 * time.Second)`,
  },
};

export default function SetupPage({ projectId, projects }: { projectId: number; projects: Project[] | null }) {
  const [lang, setLang] = useState<Lang>('browser');
  const [test, setTest] = useState<'idle' | 'sending' | 'ok' | string>('idle');
  if (!projects) return <div className="page"><Spinner /></div>;
  const project = projects.find((p) => p.id === projectId);
  if (!project) return <div className="page"><p>Проект не найден.</p></div>;
  const s = snippets[lang];

  const sendTest = async () => {
    setTest('sending');
    const id = crypto.randomUUID().replace(/-/g, '');
    const event = {
      event_id: id,
      timestamp: Date.now() / 1000,
      platform: 'javascript',
      level: 'error',
      environment: 'test',
      exception: {
        values: [
          {
            type: 'SnagTestError',
            value: 'Проверка подключения: всё работает',
            mechanism: { type: 'generic', handled: true },
            stacktrace: { frames: [{ filename: 'snag/setup.js', function: 'sendTestError', lineno: 1, in_app: true }] },
          },
        ],
      },
      breadcrumbs: [{ timestamp: Date.now() / 1000 - 1, category: 'ui.click', message: 'Нажата кнопка «Отправить тестовую ошибку»' }],
      request: { url: location.href.split('#')[0], headers: { 'User-Agent': navigator.userAgent } },
      tags: { source: 'snag-ui' },
    };
    const envelope = [JSON.stringify({ event_id: id, sent_at: new Date().toISOString() }), JSON.stringify({ type: 'event' }), JSON.stringify(event)].join('\n');
    try {
      const status = await sendEnvelope(project.id, project.public_key, envelope);
      setTest(status === 200 ? 'ok' : `Приём ответил ${status}`);
    } catch {
      setTest('Не удалось отправить');
    }
  };

  return (
    <div className="page page--narrow">
      <header className="page__head">
        <div>
          <div className="crumbs">
            <a href={`#/p/${project.id}`}>{project.name}</a> <span>/</span> Подключение
          </div>
          <h1>Подключите приложение</h1>
        </div>
      </header>

      <section className="panel">
        <h2>1. Адрес проекта (DSN)</h2>
        <p className="muted">
          Snag понимает официальные SDK Sentry. Если Sentry уже подключён, достаточно заменить DSN на этот.
        </p>
        <div className="dsn">
          <code>{project.dsn}</code>
          <CopyButton text={project.dsn} />
        </div>
      </section>

      <section className="panel">
        <div className="panel__head">
          <h2>2. Установите SDK</h2>
          <Segmented<Lang>
            label="Язык"
            value={lang}
            onChange={setLang}
            options={(Object.keys(snippets) as Lang[]).map((k) => ({ value: k, label: snippets[k].label }))}
          />
        </div>
        <div className="snippet">
          <div className="snippet__head">
            <code>{s.install}</code>
            <CopyButton text={s.install} />
          </div>
          <div className="snippet__head">
            <span className="muted">В начале программы:</span>
            <CopyButton text={s.code(project.dsn)} />
          </div>
          <pre className="json">{s.code(project.dsn)}</pre>
        </div>
      </section>

      <section className="panel">
        <h2>3. Проверьте</h2>
        <p className="muted">Кнопка отправит тестовую ошибку так же, как это делает SDK. Она сразу появится в списке проблем.</p>
        <div className="testrow">
          <button type="button" className="btn btn--primary" onClick={sendTest} disabled={test === 'sending'}>
            Отправить тестовую ошибку
          </button>
          {test === 'ok' && (
            <span className="ok">
              Принято.{' '}
              <a href={`#/p/${project.id}`} onClick={(e) => { e.preventDefault(); navigate(`/p/${project.id}`); }}>
                Открыть проблемы →
              </a>
            </span>
          )}
          {test !== 'idle' && test !== 'ok' && test !== 'sending' && <span className="login__error">{test}</span>}
        </div>
      </section>
    </div>
  );
}
