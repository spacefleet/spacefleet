import { api } from "../api/client";

// downloadComponentState saves an OpenTofu component's managed state (the
// current version) as a .tfstate file. The request needs the bearer token, so
// a plain link can't fetch it: the file is read through the API client and
// handed to the browser as a blob, under the name the server chose. Resolves
// to an error message, or null once the download has started.
export async function downloadComponentState(
  appId: string,
  componentId: string,
): Promise<string | null> {
  const { data, error, response } = await api.GET(
    "/api/applications/{id}/components/{componentId}/state/download",
    { params: { path: { id: appId, componentId } }, parseAs: "blob" },
  );
  if (error || !data) {
    return error?.message ?? "Could not download the state";
  }
  const name =
    attachmentFilename(response.headers.get("Content-Disposition")) ??
    "terraform.tfstate";
  const url = URL.createObjectURL(data);
  const a = document.createElement("a");
  a.href = url;
  a.download = name;
  document.body.appendChild(a);
  a.click();
  a.remove();
  URL.revokeObjectURL(url);
  return null;
}

// attachmentFilename reads the filename from a Content-Disposition header
// (`attachment; filename="web-infra.tfstate"`), or null when there is none.
export function attachmentFilename(header: string | null): string | null {
  const m = header?.match(/filename="?([^";]+)"?/);
  return m ? m[1] : null;
}
