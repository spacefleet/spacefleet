import { useEffect, useRef, useState } from "react";
import { Link, useLocation, useNavigate } from "react-router";
import { Check, ChevronDown, ChevronsUpDown } from "lucide-react";
import { cn } from "@/lib/utils";
import { useOrg } from "../contexts/OrgContext";
import {
  adminSection,
  areaForPath,
  navAreas,
  underPath,
  type NavArea,
  type NavLeaf,
} from "../nav";

// activeLeafPath picks the leaf the current route sits under, so a detail page
// (/clusters/nodes/…) keeps its list page (Nodes) highlighted. When leaves
// nest, the longest matching path wins.
function activeLeafPath(items: NavLeaf[], pathname: string) {
  return items
    .filter((i) => underPath(pathname, i.path))
    .reduce<string | undefined>(
      (best, i) => (best && best.length >= i.path.length ? best : i.path),
      undefined,
    );
}

// Sidebar is the app's primary navigation. The area switcher at the top picks
// which part of the platform the rail shows (Overview, Applications,
// Clusters), and the rail lists that area's pages. The area follows the
// route — switching areas navigates to the area's landing page, and following
// a link into another area switches the rail with it. Routes outside every
// area (the Admin pages) keep the area you came from. Admins also get the
// Admin section, pinned to the bottom of the rail under every area.
export function Sidebar() {
  const { pathname } = useLocation();
  const { currentRole } = useOrg();

  const routeArea = areaForPath(pathname);
  const [lastArea, setLastArea] = useState<NavArea>(routeArea ?? navAreas[0]);
  useEffect(() => {
    if (routeArea) setLastArea(routeArea);
  }, [routeArea]);
  const area = routeArea ?? lastArea;

  const isAdmin = currentRole === "admin";
  const activeAdminPath = activeLeafPath(adminSection.items, pathname);
  const [adminOpen, setAdminOpen] = useState(activeAdminPath !== undefined);
  // Keep Admin expanded when navigation lands on one of its pages (e.g. a
  // link elsewhere in the app jumps into it while it's collapsed).
  const onAdminPage = activeAdminPath !== undefined;
  useEffect(() => {
    if (onAdminPage) setAdminOpen(true);
  }, [onAdminPage]);

  function renderLeaves(items: NavLeaf[], indent: string) {
    const active = activeLeafPath(items, pathname);
    return items.map((item, i) => {
      // Render a non-interactive sub-heading above the first leaf of each
      // group (groups are contiguous in the nav config).
      const showGroup =
        item.group !== undefined && item.group !== items[i - 1]?.group;
      const isActive = item.path === active;
      return (
        <li key={item.path}>
          {showGroup && (
            <p
              className={cn(
                "px-3 pb-0.5 text-[10px] font-semibold uppercase tracking-wider text-neutral-500",
                indent,
                // Extra top space separates a group from the leaves above
                // it; a group at the very top needs less.
                i === 0 ? "pt-1" : "pt-4",
              )}
            >
              {item.group}
            </p>
          )}
          <Link
            to={item.path}
            aria-current={isActive ? "page" : undefined}
            className={cn(
              "flex items-center border-l-2 py-1.5 pr-3 text-sm hover:bg-neutral-800",
              indent,
              isActive
                ? "border-white bg-neutral-800 font-medium text-white"
                : "border-transparent text-neutral-300",
            )}
          >
            <span className="truncate">{item.label}</span>
          </Link>
        </li>
      );
    });
  }

  const AdminIcon = adminSection.icon;

  return (
    <nav
      aria-label="Primary"
      className="flex w-56 shrink-0 flex-col border-r border-neutral-800 bg-neutral-900"
    >
      <div className="border-b border-neutral-800 p-2">
        <AreaSwitcher current={area} />
      </div>

      <div className="flex flex-1 flex-col overflow-y-auto py-2">
        <ul aria-label={area.label}>{renderLeaves(area.items, "pl-4")}</ul>

        {isAdmin && (
          // Pinned to the bottom of the rail, set off by a divider.
          <div className="mt-auto border-t border-neutral-800 pt-2">
            <button
              type="button"
              onClick={() => setAdminOpen((v) => !v)}
              aria-expanded={adminOpen}
              className={cn(
                "flex w-full items-center gap-2.5 px-3 py-2 text-left text-sm font-medium hover:bg-neutral-800",
                onAdminPage ? "text-white" : "text-neutral-300",
              )}
            >
              <AdminIcon
                className="h-4 w-4 shrink-0 text-neutral-400"
                aria-hidden
              />
              <span className="truncate">{adminSection.label}</span>
              <ChevronDown
                className={cn(
                  "ml-auto h-4 w-4 shrink-0 text-neutral-500 transition-transform",
                  adminOpen ? "rotate-0" : "-rotate-90",
                )}
                aria-hidden
              />
            </button>
            {adminOpen && (
              <ul className="pb-1">
                {renderLeaves(adminSection.items, "pl-10")}
              </ul>
            )}
          </div>
        )}
      </div>
    </nav>
  );
}

// AreaSwitcher is the large dropdown at the top of the sidebar that picks
// which part of the platform the rail shows. Picking an area navigates to its
// landing page; the sidebar then follows the route.
function AreaSwitcher({ current }: { current: NavArea }) {
  const navigate = useNavigate();
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement>(null);

  // Close on outside click / Escape.
  useEffect(() => {
    if (!open) return;
    function onPointerDown(e: PointerEvent) {
      if (ref.current && !ref.current.contains(e.target as Node)) setOpen(false);
    }
    function onKeyDown(e: KeyboardEvent) {
      if (e.key === "Escape") setOpen(false);
    }
    document.addEventListener("pointerdown", onPointerDown);
    document.addEventListener("keydown", onKeyDown);
    return () => {
      document.removeEventListener("pointerdown", onPointerDown);
      document.removeEventListener("keydown", onKeyDown);
    };
  }, [open]);

  const Icon = current.icon;
  return (
    <div ref={ref} className="relative">
      <button
        type="button"
        onClick={() => setOpen((v) => !v)}
        aria-haspopup="menu"
        aria-expanded={open}
        data-testid="area-switcher"
        className="flex w-full items-center gap-2.5 border border-neutral-700 bg-neutral-800/50 p-2 text-left hover:bg-neutral-800"
      >
        <span className="flex h-8 w-8 shrink-0 items-center justify-center bg-neutral-800 text-neutral-100">
          <Icon className="h-4 w-4" aria-hidden />
        </span>
        <span className="truncate text-sm font-semibold text-neutral-100">
          {current.label}
        </span>
        <ChevronsUpDown
          className="ml-auto h-4 w-4 shrink-0 text-neutral-400"
          aria-hidden
        />
      </button>

      {open && (
        <div
          role="menu"
          aria-label="Platform areas"
          className="absolute left-0 top-full z-20 mt-1 w-72 border border-neutral-700 bg-neutral-900 py-1 shadow-lg"
        >
          {navAreas.map((a) => {
            const AreaIcon = a.icon;
            const isCurrent = a.id === current.id;
            return (
              <button
                key={a.id}
                type="button"
                role="menuitemradio"
                aria-checked={isCurrent}
                onClick={() => {
                  setOpen(false);
                  navigate(a.path);
                }}
                className="flex w-full items-center gap-2.5 px-2 py-2 text-left hover:bg-neutral-800"
              >
                <span className="flex h-8 w-8 shrink-0 items-center justify-center bg-neutral-800 text-neutral-100">
                  <AreaIcon className="h-4 w-4" aria-hidden />
                </span>
                <span className="min-w-0">
                  <span className="block truncate text-sm font-medium text-neutral-100">
                    {a.label}
                  </span>
                  <span className="block text-xs text-neutral-400">
                    {a.description}
                  </span>
                </span>
                <Check
                  className={cn(
                    "ml-auto h-4 w-4 shrink-0",
                    isCurrent ? "text-white" : "text-transparent",
                  )}
                  aria-hidden
                />
              </button>
            );
          })}
        </div>
      )}
    </div>
  );
}
