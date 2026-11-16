import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import '@/styles/global.scss';
import App from './App.tsx';

document.title = 'prox';
document.documentElement.setAttribute('translate', 'no');
document.documentElement.classList.add('notranslate');

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <App />
  </StrictMode>
);
