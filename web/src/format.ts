const compact = new Intl.NumberFormat('ru-RU', { notation: 'compact', maximumFractionDigits: 1 });
const full = new Intl.NumberFormat('ru-RU');

export function num(n: number): string {
  return n >= 10_000 ? compact.format(n) : full.format(n);
}

function plural(n: number, one: string, few: string, many: string): string {
  const m10 = n % 10;
  const m100 = n % 100;
  if (m10 === 1 && m100 !== 11) return one;
  if (m10 >= 2 && m10 <= 4 && (m100 < 12 || m100 > 14)) return few;
  return many;
}

export function count(n: number, one: string, few: string, many: string): string {
  return `${num(n)} ${plural(n, one, few, many)}`;
}

export function ago(iso: string | null | undefined, now = Date.now()): string {
  if (!iso) return '—';
  const s = Math.round((now - new Date(iso).getTime()) / 1000);
  if (s < 45) return 'только что';
  const m = Math.round(s / 60);
  if (m < 60) return `${m} мин назад`;
  const h = Math.round(m / 60);
  if (h < 24) return `${h} ч назад`;
  const d = Math.round(h / 24);
  if (d < 30) return `${d} ${plural(d, 'день', 'дня', 'дней')} назад`;
  return dateTime(iso);
}

const dtf = new Intl.DateTimeFormat('ru-RU', { day: 'numeric', month: 'short', hour: '2-digit', minute: '2-digit' });
const dtfSec = new Intl.DateTimeFormat('ru-RU', { day: 'numeric', month: 'short', year: 'numeric', hour: '2-digit', minute: '2-digit', second: '2-digit' });
const tf = new Intl.DateTimeFormat('ru-RU', { hour: '2-digit', minute: '2-digit', second: '2-digit' });

export function dateTime(iso: string, seconds = false): string {
  return (seconds ? dtfSec : dtf).format(new Date(iso));
}

export function time(iso: string): string {
  return tf.format(new Date(iso));
}

export const LEVEL_LABEL: Record<string, string> = {
  fatal: 'фатальная',
  error: 'ошибка',
  warning: 'предупреждение',
  info: 'инфо',
  debug: 'отладка',
};

export const STATUS_LABEL: Record<string, string> = {
  unresolved: 'Открыта',
  resolved: 'Решена',
  ignored: 'Игнорируется',
};

// splitTitle: «TypeError: x is undefined» → тип и текст отдельно.
export function splitTitle(title: string): [string, string] {
  const i = title.indexOf(': ');
  if (i > 0 && i < 80 && !title.slice(0, i).includes(' ')) return [title.slice(0, i), title.slice(i + 2)];
  return ['', title];
}

export function shortId(id: string): string {
  return id.slice(0, 8);
}
