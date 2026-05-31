import { useEffect, useState } from "react";
import { NavLink, useLocation } from "react-router";
import { ChevronDown } from "lucide-react";
import { cn } from "@/lib/utils";
import { navSections } from "../nav";

// Sidebar is the app's primary navigation: an expandable rail listing the six
// top-level sections, each of which opens to reveal its sub-pages. The section
// containing the current route stays expanded as you navigate; other sections
// can be toggled open/closed independently.
export function Sidebar() {
  const { pathname } = useLocation();
  const activeSection = navSections.find((s) =>
    s.items.some((i) => i.path === pathname),
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

  return (
    <nav
      aria-label="Primary"
      className="w-56 shrink-0 overflow-y-auto border-r border-gray-200 bg-white py-2"
    >
      {navSections.map((section) => {
        const isOpen = open.has(section.label);
        const isActiveSection = section === activeSection;
        const Icon = section.icon;
        return (
          <div key={section.label}>
            <button
              type="button"
              onClick={() => toggle(section.label)}
              aria-expanded={isOpen}
              className={cn(
                "flex w-full items-center gap-2.5 px-3 py-2 text-left text-sm font-medium hover:bg-gray-50",
                isActiveSection ? "text-black" : "text-gray-700",
              )}
            >
              <Icon className="h-4 w-4 shrink-0 text-gray-500" aria-hidden />
              <span className="truncate">{section.label}</span>
              <ChevronDown
                className={cn(
                  "ml-auto h-4 w-4 shrink-0 text-gray-400 transition-transform",
                  isOpen ? "rotate-0" : "-rotate-90",
                )}
                aria-hidden
              />
            </button>

            {isOpen && (
              <ul className="pb-1">
                {section.items.map((item) => (
                  <li key={item.path}>
                    <NavLink
                      to={item.path}
                      end
                      className={({ isActive }) =>
                        cn(
                          "flex items-center border-l-2 py-1.5 pl-10 pr-3 text-sm hover:bg-gray-50",
                          isActive
                            ? "border-black bg-gray-50 font-medium text-black"
                            : "border-transparent text-gray-600",
                        )
                      }
                    >
                      <span className="truncate">{item.label}</span>
                    </NavLink>
                  </li>
                ))}
              </ul>
            )}
          </div>
        );
      })}
    </nav>
  );
}
