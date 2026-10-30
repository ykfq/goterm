import { createApp } from 'vue';

import '@xterm/xterm/css/xterm.css';
import './style.css';

import App from './App.vue';
import { bootstrap } from './lib/store';

// The window mounts immediately. bootstrap() probes the Go backend, subscribes
// to the SSH event stream before any session can exist, and installs the
// Go-backed trzsz pickers (lib/fsaccess.ts) — Explorer dialogs instead of the
// webview's File System Access API, which needs a secure context and a user
// gesture it never gets here. Nothing can transfer before a session exists, so
// the pickers are in place long before anything asks for one.
//
// A failure there must not take the UI down with it: the app still renders, and
// the reason ends up in the status bar's "no backend" path.
void bootstrap().catch((error: unknown) => {
  console.error('[goterm] bootstrap failed', error);
});

createApp(App).mount('#app');
