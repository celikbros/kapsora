// Shared jsdom test setup: DOM matchers and a clean document between tests.
import '@testing-library/jest-dom/vitest';
import { afterEach } from 'vitest';
import { cleanup } from '@testing-library/react';

// jsdom has no layout; the router's scroll restoration calls this on navigation.
window.scrollTo = () => undefined;

// jsdom has no top layer. When floating-ui positions a popover it asks every ancestor
// `matches(':modal')` and `matches(':popover-open')`, and nwsapi 2.2.27 — jsdom's selector
// engine — answers `:modal` by asking itself again: forty million calls and twenty seconds
// per popover. Nothing is ever in the top layer here, so the answer is no before it asks.
const TOP_LAYER_SELECTORS = new Set([':modal', ':popover-open', ':fullscreen']);
const matches = Element.prototype.matches;
Element.prototype.matches = function (this: Element, selector: string) {
  return TOP_LAYER_SELECTORS.has(selector) ? false : matches.call(this, selector);
};

// jsdom has no PointerEvent. A hover that opens only for a mouse reads `pointerType` from
// the native event, so the tests need one that carries it.
if (typeof window.PointerEvent === 'undefined') {
  class PointerEventPolyfill extends MouseEvent {
    readonly pointerType: string;
    readonly pointerId: number;
    constructor(type: string, init: PointerEventInit = {}) {
      super(type, init);
      this.pointerType = init.pointerType ?? 'mouse';
      this.pointerId = init.pointerId ?? 1;
    }
  }
  window.PointerEvent = PointerEventPolyfill as unknown as typeof PointerEvent;
}

// jsdom has no ResizeObserver; floating popovers watch their anchor with one.
if (typeof window.ResizeObserver === 'undefined') {
  class ResizeObserverStub {
    observe() {}
    unobserve() {}
    disconnect() {}
  }
  window.ResizeObserver = ResizeObserverStub as unknown as typeof ResizeObserver;
}

afterEach(() => {
  cleanup();
});
