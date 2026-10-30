<script setup lang="ts">
import { PanelLeft, Plug, Server, TerminalSquare } from 'lucide-vue-next';
import { onBeforeUnmount, onMounted, reactive, ref } from 'vue';

import ConnectDialog from './components/ConnectDialog.vue';
import KnownHostsDialog from './components/KnownHostsDialog.vue';
import NoticeStack from './components/NoticeStack.vue';
import PassphraseDialog from './components/PassphraseDialog.vue';
import PromptDialog from './components/PromptDialog.vue';
import SidePanel from './components/SidePanel.vue';
import SettingsDialog from './components/SettingsDialog.vue';
import StatusBar from './components/StatusBar.vue';
import TabBar from './components/TabBar.vue';
import TerminalPane from './components/TerminalPane.vue';
import {
  answerPrompt,
  answerSecretPrompt,
  clearActiveBuffer,
  closeTab,
  copyActiveSelection,
  cycleTab,
  loadHosts,
  openFind,
  pasteIntoActive,
  prompt,
  reclaimFocus,
  runSelfTest,
  secretPrompt,
  setFontSize,
  state,
  terminalHasFocus,
  zoomActive,
} from './lib/store';
import { shortcutFor } from './lib/shortcuts';
import type { Host } from './lib/protocol';

const dialog = reactive({ open: false, mode: 'new' as 'new' | 'edit' | 'adhoc', host: undefined as Host | undefined });
const showKnownHosts = ref(false);
const showSettings = ref(false);

function onWindowBack(): void {
  if (document.visibilityState === 'hidden') return;
  reclaimFocus();
}

function openDialog(mode: 'new' | 'edit' | 'adhoc', host?: Host): void {
  dialog.mode = mode;
  dialog.host = host;
  dialog.open = true;
}

function closeDialog(): void {
  dialog.open = false;
  dialog.host = undefined;
}

function onKeydown(event: KeyboardEvent): void {
  // While a prompt is up it owns the keyboard: Escape belongs to it, not to the
  // dialog behind it.
  if (secretPrompt.open || prompt.open) return;

  if (event.key === 'Escape') {
    if (dialog.open) closeDialog();
    else if (showKnownHosts.value) showKnownHosts.value = false;
    else if (showSettings.value) showSettings.value = false;
    return;
  }

  // Which control keys are ours and which belong to the remote shell is decided
  // in one place (`shortcutFor`), because getting it wrong costs the user a
  // session: Ctrl+W is readline's word-rubout, not "close the tab".
  const command = shortcutFor(event, { terminalFocused: terminalHasFocus() });
  if (!command) return;
  event.preventDefault();

  switch (command) {
    case 'copy':
      void copyActiveSelection();
      break;
    case 'paste':
      void pasteIntoActive();
      break;
    case 'close-tab':
      if (state.activeTabId) closeTab(state.activeTabId);
      break;
    case 'quick-connect':
      openDialog('adhoc');
      break;
    case 'sidebar':
      state.sidebarOpen = !state.sidebarOpen;
      break;
    case 'clear':
      clearActiveBuffer();
      break;
    case 'next-tab':
      cycleTab(1);
      break;
    case 'previous-tab':
      cycleTab(-1);
      break;
    case 'settings':
      // Ctrl+, is what every settings dialog since the Mac has used.
      showSettings.value = true;
      break;
    case 'zoom-in':
      zoomActive(1);
      break;
    case 'zoom-out':
      zoomActive(-1);
      break;
    case 'zoom-reset':
      setFontSize(14);
      break;
    case 'find':
      openFind();
      break;
  }
}

onMounted(() => {
  window.addEventListener('keydown', onKeydown);
  // Coming back to the window (or to the webview) is the moment Windows can leave
  // the IME marked as disabled: the keyboard may still be pointing at a pane that
  // `v-show` has hidden, and nothing on screen is rendering the focused node. See
  // reclaimFocus() for what is and is not taken away.
  window.addEventListener('focus', onWindowBack);
  document.addEventListener('visibilitychange', onWindowBack);
  void loadHosts();
});
onBeforeUnmount(() => {
  window.removeEventListener('keydown', onKeydown);
  window.removeEventListener('focus', onWindowBack);
  document.removeEventListener('visibilitychange', onWindowBack);
});
</script>

<template>
  <div class="flex h-full w-full overflow-hidden">
    <SidePanel
      v-if="state.sidebarOpen"
      @new-host="openDialog('new')"
      @edit-host="(host) => openDialog('edit', host)"
      @quick-connect="openDialog('adhoc')"
      @show-known-hosts="showKnownHosts = true"
    />

    <main class="flex min-w-0 flex-1 flex-col">
      <TabBar @quick-connect="openDialog('adhoc')">
        <template #leading>
          <button
            class="focus-ring flex w-9 shrink-0 items-center justify-center border-r border-[var(--border)] text-[var(--text-dim)] hover:bg-[var(--panel)] hover:text-[var(--text)]"
            title="Toggle sidebar (Ctrl+Shift+B)"
            @click="state.sidebarOpen = !state.sidebarOpen"
          >
            <PanelLeft :size="14" />
          </button>
        </template>
      </TabBar>

      <section class="relative min-h-0 flex-1 bg-[var(--term-bg)]">
        <div v-for="tab in state.tabs" :key="tab.id" v-show="tab.id === state.activeTabId" class="absolute inset-0">
          <TerminalPane :tab="tab" :active="tab.id === state.activeTabId" />
        </div>

        <div v-if="!state.tabs.length" class="absolute inset-0 flex items-center justify-center overflow-auto p-8">
          <div class="w-[560px] max-w-full">
            <div class="mb-5 flex items-center gap-3">
              <span class="flex h-10 w-10 items-center justify-center rounded-xl bg-[var(--accent)]/15 text-[var(--accent)]">
                <TerminalSquare :size="20" />
              </span>
              <div>
                <h1 class="text-[17px] font-semibold tracking-tight">GoTerm</h1>
                <p class="text-[12px] text-[var(--text-dim)]">SSH terminal with drop-in trzsz file transfer.</p>
              </div>
            </div>

            <div class="grid grid-cols-2 gap-3">
              <button
                class="focus-ring flex flex-col items-start gap-1 rounded-xl border border-[var(--border)] bg-[var(--panel)] p-3 text-left hover:border-[var(--accent)]"
                @click="openDialog('new')"
              >
                <span class="flex items-center gap-2 text-[13px] font-medium">
                  <Server :size="14" class="text-[var(--accent)]" /> Save a host
                </span>
                <span class="text-[11px] leading-snug text-[var(--text-dim)]">
                  Passwords go to the OS key ring; the JSON list stays safe to back up.
                </span>
              </button>
              <button
                class="focus-ring flex flex-col items-start gap-1 rounded-xl border border-[var(--border)] bg-[var(--panel)] p-3 text-left hover:border-[var(--accent)]"
                @click="openDialog('adhoc')"
              >
                <span class="flex items-center gap-2 text-[13px] font-medium">
                  <Plug :size="14" class="text-[var(--accent)]" /> Quick connect
                </span>
                <span class="text-[11px] leading-snug text-[var(--text-dim)]">One-off session, nothing stored. Ctrl+Shift+T.</span>
              </button>
            </div>

            <ul class="mt-5 space-y-1.5 text-[12px] text-[var(--text-dim)]">
              <li>· Drag files onto a terminal to upload, or type <code class="text-[var(--text)]">trz</code> on the server.</li>
              <li>· <code class="text-[var(--text)]">tsz file</code> opens a save dialog — binary-exact, no lrzsz mojibake.</li>
              <li>· Host keys are trusted on first use; a change is refused, not warned about.</li>
              <li>· Self-test drives the real SSH stack against a local server: <button class="focus-ring rounded text-[var(--accent)] hover:underline" @click="runSelfTest">run it</button>.</li>
            </ul>

            <p v-if="!state.bridgeReady" class="mt-4 rounded-lg border border-[var(--warn)]/50 bg-[var(--warn)]/10 px-3 py-2 text-[12px] text-[var(--warn)]">
              No Go backend detected — this page is open in a plain browser, so connecting cannot work.
            </p>
          </div>
        </div>
      </section>

      <StatusBar @show-settings="showSettings = true" />
    </main>

    <NoticeStack />

    <ConnectDialog v-if="dialog.open" :mode="dialog.mode" :host="dialog.host" @close="closeDialog" @saved="loadHosts" />
    <KnownHostsDialog v-if="showKnownHosts" @close="showKnownHosts = false" />
    <SettingsDialog v-if="showSettings" @close="showSettings = false" />
    <PassphraseDialog
      v-if="secretPrompt.open"
      :title="secretPrompt.title"
      :hint="secretPrompt.hint"
      :error="secretPrompt.error"
      :rememberable="secretPrompt.rememberable"
      @submit="answerSecretPrompt"
      @cancel="answerSecretPrompt(null)"
    />
    <PromptDialog
      v-if="prompt.open"
      :title="prompt.title"
      :hint="prompt.hint"
      :label="prompt.label"
      :placeholder="prompt.placeholder"
      :confirm="prompt.confirm"
      :secret="prompt.secret"
      :mono="prompt.mono"
      :max-length="prompt.maxLength"
      :multiline="prompt.multiline"
      :initial="prompt.initial"
      :error="prompt.error"
      @submit="answerPrompt"
      @cancel="answerPrompt(null)"
    />
  </div>
</template>
