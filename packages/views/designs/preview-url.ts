export function designDraftFileUrl(tokenUrl: string, path: string): string {
  const url = new URL(tokenUrl);
  const parts = url.pathname.split("/");
  const token = parts[2];
  if (parts[1] !== "p" || !token) return tokenUrl;

  const encodedPath = path
    .split("/")
    .map((segment) => encodeURIComponent(segment))
    .join("/");
  return `${url.origin}/p/${token}/${encodedPath}`;
}
