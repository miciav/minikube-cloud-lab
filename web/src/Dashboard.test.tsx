import { render, screen, waitFor } from "@testing-library/react";
import { expect, it, vi } from "vitest";
import { Dashboard } from "./Dashboard";
it("embeds only a validated localhost dashboard endpoint", async () => {
  vi.stubGlobal(
    "fetch",
    vi.fn().mockResolvedValue(new Response('{"port":32011}')),
  );
  render(<Dashboard cluster="demo" />);
  expect(await screen.findByTitle("Kubernetes dashboard")).toHaveProperty(
    "src",
    "http://127.0.0.1:32011/",
  );
  expect(
    screen.getByRole("link", { name: "Open dashboard in a new tab" }),
  ).toHaveProperty("href", "http://127.0.0.1:32011/");
});
it("shows dashboard not ready and refuses arbitrary URLs or invalid ports", async () => {
  for (const value of [
    { port: 70000 },
    { port: 0 },
    { port: "32011" },
    { url: "https://evil.example" },
  ]) {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(new Response(JSON.stringify(value))),
    );
    const view = render(<Dashboard cluster="demo" />);
    await waitFor(() => expect(screen.getByRole("alert")).toBeTruthy());
    expect(screen.queryByTitle("Kubernetes dashboard")).toBeNull();
    view.unmount();
  }
});
