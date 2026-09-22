import { useState } from 'react';
import { api } from '../api';
import type { User } from '../types';
import { Logo } from './ui';

export default function Login({ onLogin }: { onLogin: (s: { user: User; demo: boolean }) => void }) {
  const [email, setEmail] = useState('');
  const [password, setPassword] = useState('');
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  return (
    <div className="login">
      <form
        className="login__card"
        onSubmit={async (e) => {
          e.preventDefault();
          setBusy(true);
          setError(null);
          try {
            const r = await api<{ user: User }>('POST', '/api/v1/auth/login', { email, password });
            onLogin({ user: r.user, demo: false });
          } catch (err) {
            setError((err as Error).message);
          } finally {
            setBusy(false);
          }
        }}
      >
        <Logo />
        <p className="login__lead">Ошибки ваших приложений в одном месте.</p>
        <label>
          Почта
          <input type="email" autoComplete="username" required value={email} onChange={(e) => setEmail(e.target.value)} autoFocus />
        </label>
        <label>
          Пароль
          <input type="password" autoComplete="current-password" required value={password} onChange={(e) => setPassword(e.target.value)} />
        </label>
        {error && <div className="login__error" role="alert">{error}</div>}
        <button type="submit" className="btn btn--primary" disabled={busy}>
          {busy ? 'Входим…' : 'Войти'}
        </button>
      </form>
    </div>
  );
}
