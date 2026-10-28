import { cleanup } from "@testing-library/react";
import { afterEach, vi } from "vitest";
afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});
window.matchMedia = vi
  .fn()
  .mockImplementation(() => ({
    matches: false,
    addEventListener: vi.fn(),
    removeEventListener: vi.fn(),
  }));
