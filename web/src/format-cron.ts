const days = ["Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"];

const pad = (n: string) => n.padStart(2, "0");

/** Says the common cron schedules in words; anything else stays as written. */
export function describeSchedule(cron: string): string {
  const shortcuts: Record<string, string> = {
    "@hourly": "Every hour",
    "@daily": "Every day at 00:00",
    "@midnight": "Every day at 00:00",
    "@weekly": "Every Sunday at 00:00",
    "@monthly": "On the 1st of every month",
  };
  if (shortcuts[cron.trim()]) return shortcuts[cron.trim()];
  const f = cron.trim().split(/\s+/);
  if (f.length !== 5) return cron;
  const [min, hour, dom, mon, dow] = f;
  const num = /^\d+$/;
  if (min.startsWith("*/") && hour === "*" && dom === "*" && mon === "*" && dow === "*")
    return min === "*/1" ? "Every minute" : `Every ${min.slice(2)} minutes`;
  if (min === "*" && hour === "*" && dom === "*" && mon === "*" && dow === "*") return "Every minute";
  if (num.test(min) && hour === "*" && dom === "*" && mon === "*" && dow === "*") return min === "0" ? "Every hour" : `Every hour at :${pad(min)}`;
  if (num.test(min) && hour.startsWith("*/") && dom === "*" && mon === "*" && dow === "*")
    return hour === "*/1" ? "Every hour" : `Every ${hour.slice(2)} hours`;
  if (num.test(min) && num.test(hour) && mon === "*") {
    const at = `${pad(hour)}:${pad(min)}`;
    if (dom === "*" && dow === "*") return `Every day at ${at}`;
    if (dom === "*" && /^[0-6]$/.test(dow)) return `Every ${days[Number(dow)]} at ${at}`;
    if (dom === "*" && dow === "1-5") return `Weekdays at ${at}`;
    if (num.test(dom) && dow === "*") return `On day ${dom} of every month at ${at}`;
  }
  return cron;
}
