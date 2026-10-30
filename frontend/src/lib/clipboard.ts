/*
 * Clipboard access that prefers the native side.
 *
 * WebView2 gates `navigator.clipboard.readText()` behind a permission prompt,
 * which is a terrible experience for Ctrl+Shift+V. Wails v3's runtime exposes
 * the same thing through the native API, so try that first and fall back to the
 * browser API when running in a plain browser (where the native call has
 * nowhere to go).
 */
import { Clipboard } from '@wailsio/runtime';

export async function readClipboard(): Promise<string> {
  try {
    return await Clipboard.Text();
  } catch {
    /* no backend, or the platform refused: use the web API */
  }
  try {
    return await navigator.clipboard.readText();
  } catch {
    return '';
  }
}

export async function writeClipboard(text: string): Promise<boolean> {
  try {
    await Clipboard.SetText(text);
    return true;
  } catch {
    /* fall through to the web API */
  }
  try {
    await navigator.clipboard.writeText(text);
    return true;
  } catch {
    return false;
  }
}
