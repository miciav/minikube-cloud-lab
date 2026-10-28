import { useEffect, useState } from "react";
import { request } from "./api";

// One in-flight request per resource; navigating or refreshing cancels obsolete responses.
export function useResource<T>(path: string, refresh = 0) {
  const [data, setData] = useState<T | null>(null);
  const [error, setError] = useState("");
  useEffect(() => {
    const controller = new AbortController();
    let timer: ReturnType<typeof setTimeout>;
    let pending = false;
    setData(null);
    setError("");
    async function load() {
      if (pending || document.hidden || controller.signal.aborted) return;
      pending = true;
      try {
        const value = await request<T>(path, { signal: controller.signal });
        if (!controller.signal.aborted) {
          setData(value);
          setError("");
        }
      } catch (e) {
        if (!controller.signal.aborted)
          setError(e instanceof Error ? e.message : "Cloud unavailable");
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
  return { data, error };
}
