import { useEffect, useState } from "react";
import { request } from "./api";

type Loaded<T> = { path: string; data: T | null; error: string };

// One in-flight request per resource; navigating cancels obsolete responses.
// A refresh of the same path keeps showing current data until the new response arrives.
export function useResource<T>(path: string, refresh = 0) {
  const [state, setState] = useState<Loaded<T>>({
    path,
    data: null,
    error: "",
  });
  useEffect(() => {
    const controller = new AbortController();
    let timer: ReturnType<typeof setTimeout>;
    let pending = false;
    async function load() {
      if (pending || document.hidden || controller.signal.aborted) return;
      pending = true;
      try {
        const value = await request<T>(path, { signal: controller.signal });
        if (!controller.signal.aborted)
          setState({ path, data: value, error: "" });
      } catch (e) {
        if (!controller.signal.aborted)
          setState((s) => ({
            path,
            data: s.path === path ? s.data : null,
            error: e instanceof Error ? e.message : "Cloud unavailable",
          }));
      } finally {
        pending = false;
        if (!controller.signal.aborted) timer = setTimeout(load, 15000);
      }
    }
    const visibility = () => {
      clearTimeout(timer);
      if (!document.hidden) void load();
    };
    document.addEventListener("visibilitychange", visibility);
    void load();
    return () => {
      controller.abort();
      clearTimeout(timer);
      document.removeEventListener("visibilitychange", visibility);
    };
  }, [path, refresh]);
  return state.path === path
    ? { data: state.data, error: state.error }
    : { data: null, error: "" };
}
