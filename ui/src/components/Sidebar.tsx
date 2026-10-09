import { useEffect, useMemo, useState } from "react";
import { NavLink, useLocation } from "react-router";
import { ChevronDown } from "lucide-react";
import { cn } from "@/lib/utils";
import { useOrg } from "../contexts/OrgContext";
import { navSections, type NavSection } from "../nav";

// Sidebar is the app's primary navigation: an expandable rail listing the
// top-level sections. Expandable sections open to reveal their sub-pages and
// the section containing the current route stays expanded as you navigate;
// direct-link sections (no sub-pages, e.g. Dashboard) render as a single row.
// Admin-only sections are hidden from non-admins, and footer sections (Admin)
// are pinned to the bottom of the rail.
export function Sidebar() {
  const { pathname } = useLocation();
  const { currentRole } = useOrg();
  const sections = useMemo(
    () => navSections.filter((s) => !s.adminOnly || currentRole === "admin"),
    [currentRole],
  );
  const activeSection = sections.find((s) =>
    s.items?.some((i) => i.path === pathname),
  );

  const [open, setOpen] = useState<Set<string>>(
    () => new Set(activeSection ? [activeSection.label] : []),
  );

  // Keep the active section expanded when navigation changes the route (e.g. a
  // link elsewhere in the app jumps into a collapsed section).
  const activeLabel = activeSection?.label;
  useEffect(() => {
    if (!activeLabel) return;
    setOpen((prev) =>
      prev.has(activeLabel) ? prev : new Set(prev).add(activeLabel),
    );
  }, [activeLabel]);

  function toggle(label: string) {
    setOpen((prev) => {
      const next = new Set(prev);
      if (next.has(label)) next.delete(label);
      else next.add(label);
      return next;
    });
  }

  function renderSection(section: NavSection) {
    const Icon = section.icon;

    // Direct-link section (no sub-pages): a single clickable row that owns its
    // own active state, rather than an expandable toggle.
    if (!section.items) {
      return (
        <NavLink
          key={section.label}
          to={section.path ?? "/"}
          end
          className={({ isActive }) =>
            cn(
              "flex w-full items-center gap-2.5 px-3 py-2 text-left text-sm font-medium hover:bg-neutral-800",
              isActive ? "text-white" : "text-neutral-300",
            )
          }
        >
          <Icon className="h-4 w-4 shrink-0 text-neutral-400" aria-hidden />
          <span className="truncate">{section.label}</span>
        </NavLink>
      );
    }

    const items = section.items;
    const isOpen = open.has(section.label);
    const isActiveSection = section === activeSection;
    return (
      <div key={section.label}>
        <button
          type="button"
          onClick={() => toggle(section.label)}
          aria-expanded={isOpen}
          className={cn(
            "flex w-full items-center gap-2.5 px-3 py-2 text-left text-sm font-medium hover:bg-neutral-800",
            isActiveSection ? "text-white" : "text-neutral-300",
          )}
        >
          <Icon className="h-4 w-4 shrink-0 text-neutral-400" aria-hidden />
          <span className="truncate">{section.label}</span>
          <ChevronDown
            className={cn(
              "ml-auto h-4 w-4 shrink-0 text-neutral-500 transition-transform",
              isOpen ? "rotate-0" : "-rotate-90",
            )}
            aria-hidden
          />
        </button>

        {isOpen && (
          <ul className="pb-1">
            {items.map((item, i) => {
              // Render a non-interactive sub-heading above the first leaf of
              // each group (groups are contiguous in the nav config).
              const showGroup =
                item.group !== undefined && item.group !== items[i - 1]?.group;
              return (
                <li key={item.path}>
                  {showGroup && (
                    <p
                      className={cn(
                        "px-3 pb-0.5 pl-10 text-[10px] font-semibold uppercase tracking-wider text-neutral-500",
                        // Extra top space separates a group from the leaves
                        // above it; the first group sits right under the
                        // section button so it needs less.
                        i === 0 ? "pt-1" : "pt-4",
                      )}
                    >
                      {item.group}
                    </p>
                  )}
                  <NavLink
                    to={item.path}
                    end
                    className={({ isActive }) =>
                      cn(
                        "flex items-center border-l-2 py-1.5 pl-10 pr-3 text-sm hover:bg-neutral-800",
                        isActive
                          ? "border-white bg-neutral-800 font-medium text-white"
                          : "border-transparent text-neutral-300",
                      )
                    }
                  >
                    <span className="truncate">{item.label}</span>
                  </NavLink>
                </li>
              );
            })}
          </ul>
        )}
      </div>
    );
  }

  const topSections = sections.filter((s) => !s.footer);
  const footerSections = sections.filter((s) => s.footer);

  return (
    <nav
      aria-label="Primary"
      className="flex w-56 shrink-0 flex-col overflow-y-auto border-r border-neutral-800 bg-neutral-900 py-2"
    >
      <div>{topSections.map(renderSection)}</div>
      {footerSections.length > 0 && (
        // Pinned to the bottom of the rail, set off by a divider.
        <div className="mt-auto border-t border-neutral-800 pt-2">
          {footerSections.map(renderSection)}
        </div>
      )}
    </nav>
  );
}
