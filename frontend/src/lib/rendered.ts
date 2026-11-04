/*
 * Is this element on screen?
 *
 * `v-show` hides a pane by putting `display: none` on its wrapper, not on the
 * terminal's own element, so any question about "can the user see this" has to
 * walk up the ancestors. Chromium cannot focus anything inside a subtree nobody
 * renders, and — this is the part that costs a session's input method — it says
 * nothing when it cannot. The keyboard stays where it was, in a node that is no
 * longer painted, and Windows reports that as an input method that is disabled.
 *
 * Kept in one module because two places need the same answer for a different
 * reason: the store, before it hands a terminal the keyboard, and the terminal
 * itself, before it takes the keyboard back.
 */
export function isRendered(element: Element | null): boolean {
  let node: Element | null = element;
  while (node) {
    const style = getComputedStyle(node);
    if (style.display === 'none' || style.visibility === 'hidden') return false;
    node = node.parentElement;
  }
  return true;
}
