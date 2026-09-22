import { useState } from 'react';
import { api } from '../api';
import type { Project } from '../types';

export default function NewProject({ onCreated }: { onCreated: (p: Project) => void }) {
  const [name, setName] = useState('');
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  return (
    <div className="page page--narrow">
      <header className="page__head">
        <div>
          <div className="crumbs">Проекты</div>
          <h1>Новый проект</h1>
        </div>
      </header>
      <form
        className="panel form"
        onSubmit={async (e) => {
          e.preventDefault();
          setBusy(true);
          setError(null);
          try {
            const r = await api<{ project: Project }>('POST', '/api/v1/projects', { name });
            onCreated(r.project);
          } catch (err) {
            setError((err as Error).message);
          } finally {
            setBusy(false);
          }
        }}
      >
        <label>
          Название
          <input required maxLength={100} value={name} onChange={(e) => setName(e.target.value)} placeholder="Интернет-магазин" autoFocus />
        </label>
        <p className="muted">Обычно проект — это одно приложение: сайт, бэкенд или мобильное приложение. У каждого свой DSN.</p>
        {error && <div className="login__error" role="alert">{error}</div>}
        <button type="submit" className="btn btn--primary" disabled={busy}>
          {busy ? 'Создаём…' : 'Создать и подключить'}
        </button>
      </form>
    </div>
  );
}
