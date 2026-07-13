import { App } from './App.js';
import './style.css';

const app = document.getElementById('app');
if (app) {
  new App(app);
} else {
  console.error('No #app element found');
}
