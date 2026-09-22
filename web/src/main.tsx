import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import App from './App';
import { waitForDemo } from './api';
import './styles.css';

await waitForDemo();

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <App />
  </StrictMode>,
);
