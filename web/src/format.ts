export function cpu(millicores?: number) {
  if (millicores === undefined) return "–";
  return millicores >= 1000 ? `${(millicores / 1000).toFixed(1)} cores` : `${millicores}m`;
}

export function memory(bytes?: number) {
  if (bytes === undefined) return "–";
  const gi = bytes / 1024 ** 3;
  return gi >= 1 ? `${gi.toFixed(1)} GiB` : `${Math.round(bytes / 1024 ** 2)} MiB`;
}

export function age(iso?: string) {
  if (!iso) return "";
  const s = Math.max(0, (Date.now() - new Date(iso).getTime()) / 1000);
  if (s < 60) return `${Math.floor(s)}s`;
  if (s < 3600) return `${Math.floor(s / 60)}m`;
  if (s < 86400) return `${Math.floor(s / 3600)}h`;
  return `${Math.floor(s / 86400)}d`;
}

export function percent(part: number, whole: number) {
  return whole > 0 ? Math.min(100, Math.round((part / whole) * 100)) : 0;
}
