export async function request<T>(
  path: string,
  options: RequestInit = {},
): Promise<T> {
  const response = await fetch(`/v1/${path}`, {
    ...options,
    headers: { "Content-Type": "application/json", ...options.headers },
    cache: "no-store",
  });
  if (!response.ok) {
    const error = await response
      .json()
      .catch(() => ({ error: `Request failed (${response.status})` }));
    throw Error(error.error || `Request failed (${response.status})`);
  }
  return response.json();
}

export async function downloadKubeconfig(name: string): Promise<void> {
  const response = await fetch(
    `/v1/clusters/${encodeURIComponent(name)}/kubeconfig`,
    { cache: "no-store" },
  );
  if (!response.ok) throw Error("Kubeconfig unavailable");
  const url = URL.createObjectURL(await response.blob());
  const link = document.createElement("a");
  try {
    link.href = url;
    link.download = `cloud-${name}.yaml`;
    document.body.append(link);
    link.click();
  } finally {
    link.remove();
    URL.revokeObjectURL(url);
  }
}
