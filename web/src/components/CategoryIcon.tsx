import {
  Activity,
  Box,
  Boxes,
  CalendarClock,
  Database,
  Globe,
  HardDrive,
  Inbox,
  Network,
  Search,
  ShieldCheck,
  Signpost,
  Zap,
  type LucideIcon,
} from "lucide-react";
import type { Category } from "../api";

const icons: Record<Category, LucideIcon> = {
  app: Box,
  database: Database,
  queue: Inbox,
  cache: Zap,
  search: Search,
  storage: HardDrive,
  ingress: Globe,
  network: Network,
  dns: Signpost,
  monitoring: Activity,
  security: ShieldCheck,
  platform: Boxes,
  job: CalendarClock,
};

export const categoryLabel: Record<Category, string> = {
  app: "App",
  database: "Database",
  queue: "Queue",
  cache: "Cache",
  search: "Search",
  storage: "Storage",
  ingress: "Ingress",
  network: "Network",
  dns: "DNS",
  monitoring: "Monitoring",
  security: "Security",
  platform: "Platform",
  job: "Scheduled job",
};

/** A tinted tile with the category's glyph. */
export function CategoryIcon({ category, size = 36 }: { category: Category; size?: number }) {
  const Icon = icons[category] ?? Box;
  return (
    <span
      className="cv-cat"
      style={{ width: size, height: size, color: `var(--cv-cat-${category})` }}
      aria-label={categoryLabel[category]}
      role="img"
    >
      <Icon size={Math.round(size * 0.5)} strokeWidth={2} />
    </span>
  );
}
