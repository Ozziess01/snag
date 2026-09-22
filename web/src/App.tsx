import { useCallback, useEffect, useState } from 'react';
import { api, isEmbeddedDemo, setUnauthorizedHandler } from './api';
import { navigate, useRoute } from './router';
import type { Project, User } from './types';
import { ErrorBox, Logo, Spinner } from './components/ui';
import Login from './components/Login';
import IssuesPage from './components/IssuesPage';
import IssuePage from './components/IssuePage';
import SetupPage from './components/SetupPage';
import NewProject from './components/NewProject';
import AlertsPage from './components/AlertsPage';

type Session = { user: User; demo: boolean } | null | 'loading';

export default function App() {
  const route = useRoute();
  const [session, setSession] = useState<Session>('loading');
  const [projects, setProjects] = useState<Project[] | null>(null);
  const [error, setError] = useState<string | null>(null);

  const loadProjects = useCallback(async () => {
    try {
      const r = await api<{ projects: Project[] | null }>('GET', '/api/v1/projects');
      setProjects(r.projects ?? []);
      setError(null);
    } catch (e) {
      setError((e as Error).message);
    }
  }, []);

  useEffect(() => {
    setUnauthorizedHandler(() => {
      setSession(null);
      navigate('/login', true);
    });
    api<{ user: User; demo: boolean }>('GET', '/api/v1/auth/me')
      .then((s) => setSession(s))
      .catch(() => {
        setSession(null);
        navigate('/login', true);
      });
  }, []);

  useEffect(() => {
    if (session && session !== 'loading') loadProjects();
  }, [session, loadProjects]);

  // Главная: первый проект, а если проектов нет — создание.
  useEffect(() => {
    if (route.name === 'home' && projects) {
      navigate(projects.length ? `/p/${projects[0].id}` : '/projects/new', true);
    }
  }, [route.name, projects]);

  if (session === 'loading') {
    return <div className="center"><Spinner /></div>;
  }
  if (!session || route.name === 'login') {
    return (
      <Login
        onLogin={(s) => {
          setSession(s);
          navigate('/', true);
        }}
      />
    );
  }

  const currentProject =
    route.name === 'issues' || route.name === 'setup' || route.name === 'alerts' ? route.projectId : null;

  // Во фрейме демо боковое меню лишнее: проект один, место нужнее списку.
  const embedded = isEmbeddedDemo();

  return (
    <div className={`app ${embedded ? 'app--embedded' : ''}`}>
      {!embedded && (
      <aside className="side">
        <a className="side__brand" href="#/">
          <Logo />
          {session.demo && <span className="badge badge--demo">демо</span>}
        </a>
        <div className="side__label">Проекты</div>
        <nav className="side__nav" aria-label="Проекты">
          {projects?.map((p) => (
            <a key={p.id} href={`#/p/${p.id}`} aria-current={currentProject === p.id ? 'page' : undefined}>
              <span className="side__name">{p.name}</span>
              {p.open_issues > 0 && <span className="side__count">{p.open_issues}</span>}
            </a>
          ))}
          <a href="#/projects/new" className="side__add" aria-current={route.name === 'newProject' ? 'page' : undefined}>
            + Новый проект
          </a>
        </nav>
        <div className="side__foot">
          <span className="side__user" title={session.user.email}>{session.user.email}</span>
          {!session.demo && (
            <button
              type="button"
              className="btn btn--ghost btn--sm"
              onClick={async () => {
                await api('POST', '/api/v1/auth/logout').catch(() => {});
                setSession(null);
                navigate('/login', true);
              }}
            >
              Выйти
            </button>
          )}
        </div>
      </aside>
      )}

      <main className="main">
        {error && <ErrorBox message={error} retry={loadProjects} />}
        {route.name === 'issues' && (
          <IssuesPage key={route.projectId} projectId={route.projectId} query={route.query} projects={projects} />
        )}
        {route.name === 'issue' && <IssuePage key={route.issueId} issueId={route.issueId} eventId={route.eventId} onChange={loadProjects} />}
        {route.name === 'setup' && <SetupPage projectId={route.projectId} projects={projects} />}
        {route.name === 'alerts' && <AlertsPage projectId={route.projectId} projects={projects} demo={session.demo} />}
        {route.name === 'newProject' && (
          <NewProject
            onCreated={async (p) => {
              await loadProjects();
              navigate(`/p/${p.id}/setup`);
            }}
          />
        )}
        {route.name === 'home' && !projects && <Spinner />}
      </main>
    </div>
  );
}
