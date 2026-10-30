<script setup lang="ts">
import { nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue';

import { activateTab, closeFind, closeTab, copyActiveSelection, ensureBridge, explainProgramMouse, find, getBridge, openFind, pasteIntoActive, pushNotice, reconnect, releaseBridge, setFontSize, state } from '../lib/store';
import type { Tab } from '../lib/store';

const props = defineProps<{ tab: Tab; active: boolean }>();

const hostEl = ref<HTMLDivElement | null>(null);
const menu = ref<{ x: number; y: number } | null>(null);
const menuEl = ref<HTMLDivElement | null>(null);
const transferring = ref(false);

// The find bar is one per pane but only ever shown on the active one: the store
// keeps the open flag so Ctrl+Shift+F works from anywhere, while the query and
// the match-case flag are local because each tab searches its own buffer.
const findEl = ref<HTMLInputElement | null>(null);
const findQuery = ref('');
const findCase = ref(false);

watch(
  () => find.open,
  async (open) => {
    if (!open) {
      // The pane that leaves the screen still has to forget its query: it is the
      // one the store just told to drop its highlights, and coming back to a bar
      // holding a query nobody ran would show `0 / 0` next to a live word.
      findQuery.value = '';
      findCase.value = false;
      return;
    }
    if (!props.active) return;
    await nextTick();
    findEl.value?.focus();
    findEl.value?.select();
  },
);

onMounted(() => {
  const bridge = ensureBridge(props.tab);
  if (hostEl.value) bridge.attach(hostEl.value);
  // Closing on a capture-phase pointerdown used to unmount the menu *before*
  // the item's own click handler could run, which made every entry look dead.
  // Now a press outside the menu closes it and a press inside is left alone.
  window.addEventListener('pointerdown', onPointerDownOutside, true);
  window.addEventListener('blur', closeMenu);
  window.addEventListener('keydown', onKeydown);
});

onBeforeUnmount(() => {
  window.removeEventListener('pointerdown', onPointerDownOutside, true);
  window.removeEventListener('blur', closeMenu);
  window.removeEventListener('keydown', onKeydown);
  releaseBridge(props.tab.id);
});

watch(
  () => props.active,
  (active) => {
    if (!active) return;
    const bridge = getBridge(props.tab.id);
    bridge?.fitNow();
    bridge?.focus();
  },
);

function closeMenu(): void {
  if (menu.value) menu.value = null;
}

function onPointerDownOutside(event: PointerEvent): void {
  if (!menu.value) return;
  const el = menuEl.value;
  if (el && event.target instanceof Node && el.contains(event.target)) return;
  closeMenu();
}

function onKeydown(event: KeyboardEvent): void {
  // An Escape that cancels an IME candidate window is not an Escape aimed at us.
  if (event.isComposing) return;
  if (event.key !== 'Escape') return;
  closeMenu();
  // Escape means "get rid of the topmost thing I asked for", and while the bar is
  // up that is the bar — the terminal keeps the key the moment it is gone.
  if (find.open && props.active) closeFindBar();
}

function runFind(direction: 1 | -1): void {
  const bridge = getBridge(props.tab.id);
  if (!bridge) return;
  const query = findQuery.value;
  if (!query) {
    // Deleting the last character should leave no ghost highlights behind.
    bridge.clearSearch();
    return;
  }
  const options = { caseSensitive: findCase.value };
  if (direction > 0) bridge.findNext(query, options);
  else bridge.findPrevious(query, options);
}

function toggleFindCase(): void {
  findCase.value = !findCase.value;
  if (findQuery.value) runFind(1);
}

function startFind(): void {
  openFind();
}

function closeFindBar(): void {
  closeFind();
  getBridge(props.tab.id)?.focus();
}

function onFindEscape(event: KeyboardEvent): void {
  // While an IME is composing, Escape belongs to the candidate window. Closing the
  // bar here would swallow the key and leave a half-typed composition with nowhere
  // to go.
  if (event.isComposing) return;
  event.preventDefault();
  event.stopPropagation();
  closeFindBar();
}

function openMenu(event: MouseEvent): void {
  event.preventDefault();
  // A full-screen program that asked for the mouse has already been sent this
  // click, and it draws its own menu — opening ours on top left the user with two
  // menus and neither of them usable. Shift is the modifier xterm already reserves
  // for "the local terminal, not the program" (it forces local selection), so it
  // gets this menu too.
  const bridge = getBridge(props.tab.id);
  const mouseMode = bridge?.term.modes.mouseTrackingMode ?? 'none';
  if (mouseMode !== 'none' && !event.shiftKey) {
    explainProgramMouse();
    return;
  }
  transferring.value = !!bridge?.trzsz.isTransferringFiles();
  const width = 208;
  // One item (Find) plus its divider taller than the menu that came before.
  const height = 330;
  menu.value = {
    x: Math.min(event.clientX, window.innerWidth - width - 8),
    y: Math.min(event.clientY, window.innerHeight - height - 8),
  };
}

function action(run: (bridge: NonNullable<ReturnType<typeof getBridge>>) => void): void {
  const bridge = getBridge(props.tab.id);
  closeMenu();
  if (bridge) run(bridge);
}

async function copySelection(): Promise<void> {
  closeMenu();
  await copyActiveSelection(props.tab.id);
}

async function paste(): Promise<void> {
  closeMenu();
  await pasteIntoActive(props.tab.id);
}

function pickFiles(): void {
  closeMenu();
  // The dialog is opened by the *server* side of trzsz: `trz` is sent, its
  // banner arrives, and the filter asks for files through the native picker
  // shim. Building a DataTransferItemList from a <input type=file> instead was
  // the bug behind "No files to upload": script-added items have no filesystem
  // entry, and `webkitGetAsEntry()` — what trzsz reads — returns null for them.
  const bridge = getBridge(props.tab.id);
  bridge?.requestUpload();
}

function stopTransfer(): void {
  action((bridge) => {
    if (bridge.trzsz.isTransferringFiles()) {
      bridge.trzsz.stopTransferringFiles();
      pushNotice('info', 'Stopped the file transfer.');
    }
  });
}

function zoom(step: number): void {
  const next = Math.min(28, Math.max(9, state.settings.fontSize + step));
  setFontSize(next);
}

function onWheel(event: WheelEvent): void {
  if (!event.ctrlKey) return;
  event.preventDefault();
  zoom(event.deltaY > 0 ? -1 : 1);
}

const overlayTitle = (): string => (props.tab.status === 'error' ? 'Connection failed' : 'Connection closed');

async function retry(): Promise<void> {
  closeMenu();
  await reconnect(props.tab.id);
}
</script>

<template>
  <div class="relative h-full w-full">
    <!-- Capture: xterm handles its own contextmenu and must not be able to
         swallow the event before it reaches this handler. -->
    <div
      ref="hostEl"
      data-testid="terminal-host"
      class="h-full w-full"
      @contextmenu.capture.prevent="openMenu"
      @wheel="onWheel"
      @pointerdown="activateTab(tab.id)"
    />

    <!-- In-buffer find. It sits over the terminal rather than above it so the
         prompt the user is searching for stays where they left it. -->
    <div
      v-if="find.open && active"
      data-test="find-bar"
      class="absolute right-2 top-2 z-20 flex items-center gap-1 rounded-lg border border-[var(--border)] bg-[var(--panel)]/95 px-2 py-1 text-[12px] shadow-2xl backdrop-blur"
    >
      <input
        ref="findEl"
        v-model="findQuery"
        data-test="find-input"
        class="focus-ring min-w-0 w-44 rounded bg-transparent px-1 outline-none placeholder:text-[var(--text-dim)]"
        placeholder="Find in this tab"
        spellcheck="false"
        autocomplete="off"
        @input="runFind(1)"
        @keydown.enter.prevent="runFind(1)"
        @keydown.shift.enter.prevent="runFind(-1)"
        @keydown.esc="onFindEscape"
      />
      <button
        class="focus-ring rounded px-1.5 py-0.5 text-[11px] hover:bg-[var(--panel-2)]"
        :class="findCase ? 'text-[var(--accent)]' : 'text-[var(--text-dim)]'"
        title="Match case"
        data-test="find-case"
        @click="toggleFindCase"
      >
        Aa
      </button>
      <span class="min-w-[3.25rem] text-center tabular-nums text-[var(--text-dim)]" data-test="find-count">
        {{ find.count ? `${find.index || '?'} / ${find.count}` : '0 / 0' }}
      </span>
      <button class="focus-ring rounded px-1.5 py-0.5 text-[var(--text-dim)] hover:bg-[var(--panel-2)] hover:text-[var(--text)]" title="Previous match (Shift+Enter)" data-test="find-prev" @click="runFind(-1)">
        Prev
      </button>
      <button class="focus-ring rounded px-1.5 py-0.5 text-[var(--text-dim)] hover:bg-[var(--panel-2)] hover:text-[var(--text)]" title="Next match (Enter)" data-test="find-next" @click="runFind(1)">
        Next
      </button>
      <button class="focus-ring rounded px-1.5 py-0.5 text-[var(--text-dim)] hover:bg-[var(--panel-2)] hover:text-[var(--text)]" title="Close (Esc)" data-test="find-close" @click="closeFindBar">
        Esc
      </button>
    </div>

    <!-- Above the terminal's own layers: a canvas or a scroll viewport must
         never get between the pointer and these two buttons. -->
    <div
      v-if="tab.status === 'closed' || tab.status === 'error'"
      class="absolute inset-x-0 bottom-0 z-10 flex items-center gap-3 border-t border-[var(--border)] bg-[var(--panel)]/95 px-4 py-2 text-[12px] backdrop-blur"
    >
      <span class="font-medium">{{ overlayTitle() }}</span>
      <span class="min-w-0 flex-1 truncate-1 text-[var(--text-dim)]">{{ tab.detail || 'the remote side ended the session' }}</span>
      <button
        v-if="tab.hostId"
        class="focus-ring rounded bg-[var(--accent)] px-2.5 py-1 text-[12px] font-medium text-[var(--accent-ink)]"
        @click="retry"
      >
        Reconnect
      </button>
      <button class="focus-ring rounded border border-[var(--border)] px-2.5 py-1 text-[12px] text-[var(--text-dim)] hover:text-[var(--text)]" @click="closeTab(tab.id)">
        Close tab
      </button>
    </div>

    <div
      v-if="menu"
      ref="menuEl"
      class="fixed z-50 w-52 overflow-hidden rounded-lg border border-[var(--border)] bg-[var(--panel)] py-1 text-[12px] shadow-2xl"
      :style="{ left: menu.x + 'px', top: menu.y + 'px' }"
      @pointerdown.stop
    >
      <button class="flex w-full items-center justify-between px-3 py-1.5 text-left hover:bg-[var(--panel-2)]" data-test="menu-find" @click="action(startFind)">
        Find in terminal… <span class="text-[var(--text-dim)]">Ctrl+Shift+F</span>
      </button>
      <div class="my-1 h-px bg-[var(--border)]" />
      <button class="flex w-full items-center justify-between px-3 py-1.5 text-left hover:bg-[var(--panel-2)]" @click="action((b) => b.term.selectAll())">
        Select all <span class="text-[var(--text-dim)]">—</span>
      </button>
      <button class="flex w-full items-center justify-between px-3 py-1.5 text-left hover:bg-[var(--panel-2)]" @click="copySelection">
        Copy <span class="text-[var(--text-dim)]">Ctrl+Shift+C</span>
      </button>
      <button class="flex w-full items-center justify-between px-3 py-1.5 text-left hover:bg-[var(--panel-2)]" @click="paste">
        Paste <span class="text-[var(--text-dim)]">Ctrl+Shift+V</span>
      </button>
      <div class="my-1 h-px bg-[var(--border)]" />
      <button class="flex w-full items-center justify-between px-3 py-1.5 text-left hover:bg-[var(--panel-2)]" @click="pickFiles">
        Upload files… <span class="text-[var(--text-dim)]">trz</span>
      </button>
      <button
        class="flex w-full items-center px-3 py-1.5 text-left hover:bg-[var(--panel-2)]"
        :class="{ 'opacity-45': !transferring }"
        @click="stopTransfer"
      >
        Stop transfer
      </button>
      <button class="flex w-full items-center justify-between px-3 py-1.5 text-left hover:bg-[var(--panel-2)]" @click="action((b) => b.clear())">
        Clear buffer <span class="text-[var(--text-dim)]">—</span>
      </button>
      <div class="my-1 h-px bg-[var(--border)]" />
      <button class="flex w-full items-center justify-between px-3 py-1.5 text-left hover:bg-[var(--panel-2)]" @click="zoom(1)">
        Zoom in <span class="text-[var(--text-dim)]">Ctrl +</span>
      </button>
      <button class="flex w-full items-center justify-between px-3 py-1.5 text-left hover:bg-[var(--panel-2)]" @click="zoom(-1)">
        Zoom out <span class="text-[var(--text-dim)]">Ctrl −</span>
      </button>
      <button class="flex w-full items-center px-3 py-1.5 text-left hover:bg-[var(--panel-2)]" @click="action((b) => b.fitNow())">
        Fit to window
      </button>
      <button
        v-if="tab.hostId"
        class="flex w-full items-center px-3 py-1.5 text-left text-[var(--accent)] hover:bg-[var(--panel-2)]"
        @click="retry"
      >
        Reconnect
      </button>
    </div>
  </div>
</template>
