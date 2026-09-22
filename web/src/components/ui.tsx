import { useState, type ReactNode } from 'react';
import { LEVEL_LABEL, num } from '../format';

export function Segmented<T extends string>(props: {
  value: T;
  options: { value: T; label: ReactNode }[];
  onChange: (v: T) => void;
  label: string;
}) {
  return (
    <div className="segmented" role="radiogroup" aria-label={props.label}>
      {props.options.map((o) => (
        <button
          key={o.value}
          type="button"
          role="radio"
          aria-checked={o.value === props.value}
          className={o.value === props.value ? 'on' : ''}
          onClick={() => props.onChange(o.value)}
        >
          {o.label}
        </button>
      ))}
    </div>
  );
}

export function LevelBadge({ level }: { level: string }) {
  return <span className={`badge level-${level}`}>{LEVEL_LABEL[level] ?? level}</span>;
}

export function Spinner({ label = 'Загрузка…' }: { label?: string }) {
  return (
    <div className="spinner" role="status">
      <span className="spinner__dot" />
      {label}
    </div>
  );
}

export function ErrorBox({ message, retry }: { message: string; retry?: () => void }) {
  return (
    <div className="errorbox" role="alert">
      <span>{message}</span>
      {retry && (
        <button type="button" className="btn btn--ghost" onClick={retry}>
          Повторить
        </button>
      )}
    </div>
  );
}

export function Empty({ title, children }: { title: string; children?: ReactNode }) {
  return (
    <div className="empty">
      <svg viewBox="0 0 48 48" aria-hidden="true">
        <circle cx="24" cy="24" r="20" />
        <path d="M16 25l6 6 11-13" />
      </svg>
      <h3>{title}</h3>
      {children && <div className="empty__text">{children}</div>}
    </div>
  );
}

export function CopyButton({ text, label = 'Копировать' }: { text: string; label?: string }) {
  const [done, setDone] = useState(false);
  return (
    <button
      type="button"
      className="btn btn--ghost btn--sm"
      onClick={async () => {
        try {
          await navigator.clipboard.writeText(text);
        } catch {
          // Без HTTPS clipboard может быть недоступен — выделяем вручную не будем, просто молчим.
        }
        setDone(true);
        setTimeout(() => setDone(false), 1500);
      }}
    >
      {done ? 'Скопировано' : label}
    </button>
  );
}

// Sparkline — маленький график в строке списка.
export function Sparkline({ buckets, title }: { buckets: number[]; title: (i: number) => string }) {
  const max = Math.max(1, ...buckets);
  return (
    <div className="spark" aria-hidden="true">
      {buckets.map((v, i) => (
        <span key={i} title={title(i)} style={{ height: `${v === 0 ? 2 : 12 + (v / max) * 88}%` }} className={v === 0 ? 'zero' : ''} />
      ))}
    </div>
  );
}

// Bars — большой график на странице проблемы.
export function Bars({ buckets, label }: { buckets: number[]; label: (i: number) => string }) {
  const max = Math.max(1, ...buckets);
  return (
    <div className="bars">
      <div className="bars__grid">
        <span>{num(max)}</span>
        <span>{max >= 4 ? num(Math.round(max / 2)) : ''}</span>
        <span>0</span>
      </div>
      <div className="bars__cols">
        {buckets.map((v, i) => (
          <div key={i} className="bars__col" title={`${label(i)}: ${num(v)}`}>
            <span style={{ height: `${(v / max) * 100}%` }} className={v === 0 ? 'zero' : ''} />
          </div>
        ))}
      </div>
    </div>
  );
}

export function Logo() {
  return (
    <span className="logo">
      <svg viewBox="0 0 32 32" aria-hidden="true">
        <rect width="32" height="32" rx="8" />
        <path d="M10 11h9a3 3 0 010 6h-6a3 3 0 000 6h9" />
      </svg>
      snag
    </span>
  );
}
