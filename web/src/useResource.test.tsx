import { act, renderHook, waitFor } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { useResource } from "./useResource";
afterEach(() => vi.useRealTimers());
it("ignores late response after cluster switch", async () => {
  let oldResolve!: (v: Response) => void;
  const fetch = vi
    .fn()
    .mockImplementationOnce(
      () =>
        new Promise<Response>((resolve) => {
          oldResolve = resolve;
        }),
    )
    .mockResolvedValueOnce(new Response(JSON.stringify({ name: "new" })));
  vi.stubGlobal("fetch", fetch);
  const { result, rerender } = renderHook(
    ({ path }) => useResource<{ name: string }>(path),
    { initialProps: { path: "clusters/old/monitoring" } },
  );
  rerender({ path: "clusters/new/monitoring" });
  await waitFor(() => expect(result.current.data?.name).toBe("new"));
  await act(async () =>
    oldResolve(new Response(JSON.stringify({ name: "old" }))),
  );
  expect(result.current.data?.name).toBe("new");
  expect(fetch.mock.calls[0][1].signal.aborted).toBe(true);
});
it("does not overlap polling and pauses while hidden", async () => {
  vi.useFakeTimers();
  let resolve!: (v: Response) => void;
  const fetch = vi.fn().mockImplementation(
    () =>
      new Promise<Response>((r) => {
        resolve = r;
      }),
  );
  vi.stubGlobal("fetch", fetch);
  const hidden = vi.spyOn(document, "hidden", "get").mockReturnValue(false);
  const { unmount } = renderHook(() => useResource("clusters"));
  await act(async () => {
    await vi.advanceTimersByTimeAsync(45000);
  });
  expect(fetch).toHaveBeenCalledTimes(1);
  await act(async () => {
    resolve(new Response("[]"));
  });
  hidden.mockReturnValue(true);
  await act(async () => {
    await vi.advanceTimersByTimeAsync(15000);
  });
  expect(fetch).toHaveBeenCalledTimes(1);
  hidden.mockReturnValue(false);
  await act(async () => document.dispatchEvent(new Event("visibilitychange")));
  expect(fetch).toHaveBeenCalledTimes(2);
  unmount();
  expect(fetch.mock.calls[1][1].signal.aborted).toBe(true);
});
