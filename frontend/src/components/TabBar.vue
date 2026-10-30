<script setup lang="ts">
import { Plus, X } from 'lucide-vue-next';
import { computed, onBeforeUnmount, onMounted, ref } from 'vue';

import { activateTab, closeTab, duplicateTab, moveTab, reconnect, state } from '../lib/store';
import type { Tab } from '../lib/store';

const emit = defineEmits<{ (event: 'quick-connect'): void }>();

const dot: Record<Tab['status'], string> = {
  open: 'bg-[var(--ok)]',
  connecting: 'bg-[var(--warn)] pulse',
  error: 'bg-[var(--err)]',
  closed: 'bg-[var(--text-dim)] opacity-60',
};

function onWheel(event: WheelEvent): void {
  if (!event.deltaY) return;
  event.preventDefault();
  (event.currentTarget as HTMLElement).scrollLeft += event.deltaY;
}

// --- drag to reorder --------------------------------------------------------
//
// Tabs are reordered live: hovering another tab while dragging moves the dragged
// one into its slot, so the strip is always showing the result rather than a
// preview. Only `state.tabs` changes — the sessions are keyed by id, so nothing
// behind the tabs is disturbed.
const draggingId = ref<string | null>(null);
const dropTargetId = ref<string | null>(null);

function onDragStart(event: DragEvent, tab: Tab): void {
  draggingId.value = tab.id;
  dropTargetId.value = null;
  if (event.dataTransfer) {
    event.dataTransfer.effectAllowed = 'move';
    // Firefox needs some payload before it will start a drag at all.
    event.dataTransfer.setData('text/plain', tab.id);
  }
}

function onDragOver(event: DragEvent, tab: Tab): void {
  const id = draggingId.value;
  if (!id || id === tab.id) return;
  // Claiming the event is what turns the cursor into a drop arrow instead of a
  // "no" sign, and what lets the drop land here.
  event.preventDefault();
  if (event.dataTransfer) event.dataTransfer.dropEffect = 'move';
  dropTargetId.value = tab.id;
  moveTab(id, tab.id);
}

function onDrop(event: DragEvent): void {
  event.preventDefault();
  draggingId.value = null;
  dropTargetId.value = null;
}

function onDragEnd(): void {
  draggingId.value = null;
  dropTargetId.value = null;
}

// --- tab menu ---------------------------------------------------------------
//
// Right-click a tab. Duplicate opens a second terminal on the connection that
// is already up, so nothing is logged in twice; Reconnect re-dials a saved host.
// Duplicate is listed first because it is what an open tab is asked for most.
const menu = ref<{ x: number; y: number; tabId: string } | null>(null);
const menuEl = ref<HTMLDivElement | null>(null);

const menuTab = computed(() => state.tabs.find((tab) => tab.id === menu.value?.tabId));
/** Ad-hoc sessions kept no password, so there is nothing to dial again. */
const canReconnect = computed(() => Boolean(menuTab.value?.hostId));
const canDuplicate = computed(() => menuTab.value?.status === 'open' && Boolean(menuTab.value?.sessionId));

function openTabMenu(event: MouseEvent, tab: Tab): void {
  event.preventDefault();
  activateTab(tab.id);
  const width = 210;
  const height = 88;
  menu.value = {
    x: Math.min(event.clientX, window.innerWidth - width - 8),
    y: Math.min(event.clientY, window.innerHeight - height - 8),
    tabId: tab.id,
  };
}

function closeMenu(): void {
  menu.value = null;
}

function run(tabId: string | undefined, action: (id: string) => unknown): void {
  // The id is captured before the menu closes: the closure would otherwise read
  // `menu.tabId` off a menu that is already gone.
  closeMenu();
  if (!tabId) return;
  void action(tabId);
}

function onPointerDownOutside(event: PointerEvent): void {
  if (!menu.value) return;
  const el = menuEl.value;
  if (el && event.target instanceof Node && el.contains(event.target)) return;
  closeMenu();
}

function onKeydown(event: KeyboardEvent): void {
  if (event.key === 'Escape') closeMenu();
}

onMounted(() => {
  // Capture phase, but a press *inside* the menu is left alone: closing it here
  // would unmount the entry before its own click handler ran.
  window.addEventListener('pointerdown', onPointerDownOutside, true);
  window.addEventListener('keydown', onKeydown);
  window.addEventListener('blur', closeMenu);
});

onBeforeUnmount(() => {
  window.removeEventListener('pointerdown', onPointerDownOutside, true);
  window.removeEventListener('keydown', onKeydown);
  window.removeEventListener('blur', closeMenu);
});
</script>

<template>
  <div class="flex h-9 shrink-0 items-stretch border-b border-[var(--border)] bg-[var(--panel-2)]">
    <slot name="leading" />
    <div class="flex min-w-0 flex-1 items-stretch overflow-x-auto" @wheel="onWheel">
      <div
        v-for="tab in state.tabs"
        :key="tab.id"
        class="group flex max-w-[260px] min-w-[140px] cursor-pointer items-center gap-2 border-r border-[var(--border)] px-3 text-[12px]"
        :class="[
          tab.id === state.activeTabId
            ? 'bg-[var(--bg)] text-[var(--text)]'
            : 'text-[var(--text-dim)] hover:bg-[var(--panel)]',
          draggingId === tab.id ? 'cursor-grabbing opacity-50' : '',
          dropTargetId === tab.id ? 'shadow-[inset_2px_0_0_var(--accent)]' : '',
        ]"
        :title="tab.target"
        draggable="true"
        data-test="tab"
        @click="activateTab(tab.id)"
        @contextmenu.prevent="openTabMenu($event, tab)"
        @mousedown.middle.prevent="closeTab(tab.id)"
        @dragstart="onDragStart($event, tab)"
        @dragover="onDragOver($event, tab)"
        @drop="onDrop"
        @dragend="onDragEnd"
      >
        <span class="h-1.5 w-1.5 shrink-0 rounded-full" :class="dot[tab.status]" />
        <span class="truncate-1 flex-1">{{ tab.title }}</span>
        <button
          class="focus-ring rounded p-0.5 opacity-0 hover:bg-[var(--border)] group-hover:opacity-100"
          :class="{ 'opacity-100': tab.id === state.activeTabId }"
          title="Close tab (Ctrl+Shift+W)"
          @click.stop="closeTab(tab.id)"
        >
          <X :size="12" />
        </button>
      </div>
    </div>
    <button
      class="focus-ring flex w-9 shrink-0 items-center justify-center text-[var(--text-dim)] hover:bg-[var(--panel)] hover:text-[var(--text)]"
      title="New connection (Ctrl+Shift+T)"
      @click="emit('quick-connect')"
    >
      <Plus :size="15" />
    </button>

    <div
      v-if="menu"
      ref="menuEl"
      class="fixed z-50 w-[210px] overflow-hidden rounded-lg border border-[var(--border)] bg-[var(--panel)] py-1 text-[12px] shadow-2xl"
      :style="{ left: `${menu.x}px`, top: `${menu.y}px` }"
      data-test="tab-menu"
      @pointerdown.stop
    >
      <button
        class="flex w-full items-center justify-between px-3 py-1.5 text-left hover:bg-[var(--panel-2)]"
        :class="{ 'opacity-45': !canDuplicate, 'cursor-not-allowed': !canDuplicate }"
        :disabled="!canDuplicate"
        :title="canDuplicate ? 'Another terminal on this connection, without logging in again' : 'Only a connected session can be duplicated'"
        data-test="tab-duplicate"
        @click="run(menu?.tabId, duplicateTab)"
      >
        Duplicate
      </button>
      <button
        class="flex w-full items-center justify-between px-3 py-1.5 text-left hover:bg-[var(--panel-2)]"
        :class="{ 'opacity-45': !canReconnect, 'cursor-not-allowed': !canReconnect }"
        :disabled="!canReconnect"
        :title="canReconnect ? 'Dial this host again' : 'Ad-hoc sessions kept no password to dial again'"
        data-test="tab-reconnect"
        @click="run(menu?.tabId, reconnect)"
      >
        Reconnect
      </button>
    </div>
  </div>
</template>
