// Evaluate this function in a fresh preview tab (e.g. Chrome DevTools).
// Test-only JavaScript: the shipped component/controller is entirely Go.
async function probeProgress() {
  const wait = ms => new Promise(resolve => setTimeout(resolve, ms));
  const assert = (ok, message) => { if (!ok) throw new Error(message); };
  const button = action => document.querySelector('#control-' + action);
  const click = async action => { button(action).click(); await wait(100); };
  for (let i = 0; i < 100 && !button('pause'); i++) await wait(100);
  assert(button('pause'), 'WASM did not mount');
  if (!button('pause').textContent.includes('Resume')) await click('pause');
  await click('restart');
  const root = document.querySelector('#progress');
  const paused = document.querySelector('.monitor').textContent;
  await wait(600);
  assert(paused === document.querySelector('.monitor').textContent, 'pause did not freeze playback');
  root.focus();
  root.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowDown', bubbles: true, cancelable: true }));
  await wait(100);
  assert(root.dataset.selected === 'activity-1', 'keyboard selection');
  assert(document.activeElement.id === 'activity-1', 'selection/focus diverged');
  document.activeElement.click();
  await wait(100);
  assert(document.querySelector('#activity-1').getAttribute('aria-expanded') === 'true', 'expansion');
  await click('pause');
  const before = document.querySelector('.stats').textContent;
  await wait(1200);
  assert(before !== document.querySelector('.stats').textContent, 'data did not advance');
  assert(root.dataset.selected === 'activity-1', 'live update lost selection');
  await click('motion');
  const reduced = button('motion').getAttribute('aria-pressed') === 'true';
  assert(reduced && document.querySelector('.marker').textContent === '●', 'reduced motion');
  await click('motion');
  await click('failure');
  for (let i = 0; i < 3; i++) await click('speed');
  assert(button('speed').textContent.includes('4×'), 'speed control');
  for (let i = 0; i < 130 && root.dataset.status !== 'FAILED'; i++) await wait(100);
  assert(root.dataset.status === 'FAILED', 'failure scenario did not arrive');
  assert(document.querySelector('#activity-7').textContent.includes('blocked'), 'blocked dependent missing');
  await click('completed');
  assert(document.querySelector('#activity-5'), 'failed activity was hidden');
  assert(!document.querySelector('#activity-0'), 'completed activity was not hidden');
  await click('pause');
  assert(document.documentElement.scrollWidth <= innerWidth, 'horizontal overflow');
  return { pause: true, keyboardFocus: true, expansion: true, liveUpdates: true, selectionRetention: true, motion: true, speed: true, failure: true, filtering: true, noOverflow: true };
}
