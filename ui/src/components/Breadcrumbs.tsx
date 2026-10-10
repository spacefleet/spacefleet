import type { ReactNode } from "react";
import { Link } from "react-router";
import { ChevronRight } from "lucide-react";

// Crumb is one step of a page's breadcrumb trail: a page to go back up to
// (`to`), or — for a sidebar section with no page of its own, like Admin — a
// plain label.
export interface Crumb {
  label: ReactNode;
  to?: string;
}

// Breadcrumbs is the trail at the top of every page in the app shell — the
// app's one way "up": the pages above this one, most general first, each a
// link. The trail stops at the parent; the page's own <h1> just below is its
// last step, so a top-level page shows only its sidebar section. onNavigate
// runs before a crumb is followed (the component editor uses it to discard a
// component that was never saved).
export function Breadcrumbs({
  items,
  onNavigate,
}: {
  items: Crumb[];
  onNavigate?: () => void;
}) {
  return (
    <nav aria-label="Breadcrumb">
      <ol className="flex flex-wrap items-center gap-x-1.5 gap-y-1 text-sm">
        {items.map((crumb, i) => (
          <li key={i} className="flex min-w-0 items-center gap-1.5">
            {i > 0 && (
              <ChevronRight
                className="h-3.5 w-3.5 shrink-0 text-neutral-600"
                aria-hidden
              />
            )}
            {crumb.to ? (
              <Link
                to={crumb.to}
                onClick={onNavigate}
                className="truncate text-neutral-400 hover:text-neutral-100"
              >
                {crumb.label}
              </Link>
            ) : (
              <span className="truncate text-neutral-500">{crumb.label}</span>
            )}
          </li>
        ))}
      </ol>
    </nav>
  );
}
