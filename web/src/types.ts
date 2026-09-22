// Типы ответов API (internal/api). Пустые списки Go присылает как null.

export type Level = 'fatal' | 'error' | 'warning' | 'info' | 'debug';
export type Status = 'unresolved' | 'resolved' | 'ignored';
export type PeriodName = '24h' | '14d';

export interface User {
  id: number;
  email: string;
}

export interface Project {
  id: number;
  name: string;
  slug: string;
  public_key: string;
  allowed_origins: string[] | null;
  created_at: string;
  dsn: string;
  open_issues: number;
}

export interface Issue {
  id: number;
  project_id: number;
  grouping: string;
  title: string;
  culprit: string;
  level: Level;
  platform: string;
  status: Status;
  first_seen: string;
  last_seen: string;
  times_seen: number;
  resolved_at: string | null;
  regressed_at: string | null;
}

export interface IssueRow extends Issue {
  events: number;
  users: number;
  buckets: number[];
}

export interface Period {
  name: PeriodName;
  since: string;
  until: string;
  step: number;
}

export interface StatsBlock {
  period: Period;
  events: number;
  users: number;
  buckets: number[];
}

export interface TagGroup {
  key: string;
  total: number;
  values: { value: string; count: number }[];
}

export interface IssueDetail {
  issue: Issue;
  project: Project;
  stats_24h: StatsBlock;
  stats_14d: StatsBlock;
  tags: TagGroup[];
}

export interface Frame {
  filename: string;
  abs_path: string;
  function: string;
  module: string;
  lineno: number;
  colno: number;
  in_app: boolean;
  context: { line: number; code: string }[] | null;
}

export interface ExceptionInfo {
  type: string;
  value: string;
  module: string;
  mechanism: { type: string; handled: boolean | null } | null;
  frames: Frame[] | null;
}

export interface Breadcrumb {
  timestamp: string | null;
  type: string;
  category: string;
  message: string;
  level: string;
  data: Record<string, unknown> | null;
}

export interface EventDetail {
  id: string;
  timestamp: string;
  received_at: string;
  level: Level;
  platform: string;
  environment: string;
  release: string;
  title: string;
  culprit: string;
  message: string;
  sdk: { name: string; version: string } | null;
  exceptions: ExceptionInfo[] | null;
  stacktrace: Frame[] | null;
  breadcrumbs: Breadcrumb[] | null;
  request: {
    url: string;
    method: string;
    headers: [string, string][] | null;
    query_string: unknown;
    data: unknown;
  } | null;
  user: { id: string; email: string; username: string; ip_address: string } | null;
  contexts: Record<string, Record<string, unknown>> | null;
  extra: Record<string, unknown> | null;
  tags: [string, string][] | null;
  fingerprint: string[] | null;
  raw: unknown;
}

export interface Channel {
  id: number;
  project_id: number;
  kind: 'telegram';
  target: string;
  on_new: boolean;
  on_regression: boolean;
  spike_threshold: number;
  spike_window: number;
  created_at: string;
}

export interface EventSummary {
  id: string;
  timestamp: string;
  level: Level;
  release: string;
  environment: string;
  user: string;
}
