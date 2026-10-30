<script setup lang="ts">
import { CircleDot, ShieldAlert, X, Zap } from 'lucide-vue-next';
import { dismissNotice, enableAntiIdle, state, type Notice } from '../lib/store';

function icon(level: string) {
  if (level === 'error') return ShieldAlert;
  if (level === 'warn') return CircleDot;
  return Zap;
}

/** The one affordance a notice may carry; adding another is a line here. */
function act(notice: Notice): void {
  if (notice.action === 'enable-anti-idle') enableAntiIdle();
  dismissNotice(notice.id);
}
</script>

<template>
  <!--
    Top-right, not bottom-right. The bottom-right corner belongs to whatever the
    session itself puts there — the "Connection closed" bar with its Reconnect
    and Close buttons, and the status-bar controls. A toast anchored there is
    pointer-events-auto and would swallow those clicks for as long as it lives
    (errors never auto-dismiss), which is exactly how a closed tab's Reconnect
    button stops responding.
  -->
  <div
    data-test="notice-stack"
    class="pointer-events-none fixed right-3 top-11 z-50 flex max-h-[70vh] w-[min(560px,calc(100vw-24px))] flex-col gap-2 overflow-y-auto"
  >
    <TransitionGroup name="notice">
      <div
        v-for="notice in state.notices"
        :key="notice.id"
        class="toast-in pointer-events-auto flex items-start gap-2 rounded-lg border px-3 py-2 shadow-lg backdrop-blur"
        :class="
          notice.level === 'error'
            ? 'border-[var(--err)]/60 bg-[var(--panel)]/95'
            : notice.level === 'warn'
              ? 'border-[var(--warn)]/50 bg-[var(--panel)]/95'
              : 'border-[var(--border)] bg-[var(--panel)]/95'
        "
      >
        <component
          :is="icon(notice.level)"
          :size="14"
          class="mt-0.5 shrink-0"
          :class="notice.level === 'error' ? 'text-[var(--err)]' : notice.level === 'warn' ? 'text-[var(--warn)]' : 'text-[var(--accent)]'"
        />
        <div class="min-w-0 flex-1 whitespace-pre-wrap break-words leading-snug">{{ notice.message }}</div>
        <button
          v-if="notice.action"
          class="focus-ring shrink-0 self-center rounded border border-[var(--accent)]/60 px-2 py-0.5 text-[11px] text-[var(--accent)] hover:bg-[var(--accent)]/10"
          data-test="notice-action"
          @click="act(notice)"
        >
          Turn on
        </button>
        <button class="focus-ring shrink-0 rounded p-0.5 text-[var(--text-dim)] hover:text-[var(--text)]" title="Dismiss" @click="dismissNotice(notice.id)">
          <X :size="13" />
        </button>
      </div>
    </TransitionGroup>
  </div>
</template>

<style scoped>
.notice-move,
.notice-enter-active,
.notice-leave-active {
  transition: all 160ms ease;
}
.notice-leave-to {
  opacity: 0;
  transform: translateX(12px);
}
</style>
