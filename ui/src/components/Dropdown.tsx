import {
  useEffect,
  useLayoutEffect,
  useRef,
  useState,
  type CSSProperties,
  type ReactNode,
} from "react";
import { createPortal } from "react-dom";

// DropdownItem describes one entry in a Dropdown menu: a visible label, an
// optional leading icon, and the action fired on click.
export interface DropdownItem {
  label: string;
  icon?: ReactNode;
  onSelect: () => void;
  // A disabled entry still lists (so the menu's shape is stable) but can't be
  // picked — e.g. "Move left" on the first stage.
  disabled?: boolean;
  // A destructive entry renders in red.
  danger?: boolean;
  // A line under the label — e.g. why a disabled entry can't be picked.
  hint?: string;
}

// Dropdown is a small self-contained button + menu. It closes on outside click,
// on Escape, and when the page scrolls or resizes; matches the toolbar button
// styling (sharp corners, neutral palette); and drives its menu from an items
// array so adding an entry is a single line at the call site. The menu renders
// in a portal with fixed positioning, so a scrolling or overflow-clipped
// ancestor (the stage builder's horizontal scroller) can't cut it off; it opens
// below the trigger, or above it when there's no room below. Deliberately
// minimal — we avoid pulling in a heavyweight primitive for a few menus.
export function Dropdown({
  trigger,
  items,
  align = "right",
  label,
  triggerClassName = "inline-flex items-center gap-1.5 border border-neutral-700 px-3 py-1.5 text-sm text-neutral-300 hover:bg-neutral-800",
}: {
  trigger: ReactNode;
  items: DropdownItem[];
  align?: "left" | "right";
  // Accessible name for an icon-only trigger.
  label?: string;
  // Overrides the trigger's styling (e.g. a borderless icon button).
  triggerClassName?: string;
}) {
  const [open, setOpen] = useState(false);
  const rootRef = useRef<HTMLDivElement>(null);
  const buttonRef = useRef<HTMLButtonElement>(null);
  const menuRef = useRef<HTMLDivElement>(null);
  const [position, setPosition] = useState<CSSProperties>({});

  // Place the menu against the trigger once it's rendered (so its real height
  // is known): below, or above when the space below is too short.
  useLayoutEffect(() => {
    if (!open || !buttonRef.current) return;
    const trigger = buttonRef.current.getBoundingClientRect();
    const height = menuRef.current?.offsetHeight ?? 0;
    const gap = 4;
    const below = trigger.bottom + gap + height <= window.innerHeight;
    const vertical =
      below || trigger.top - gap - height < 0
        ? { top: trigger.bottom + gap }
        : { top: trigger.top - gap - height };
    setPosition(
      align === "right"
        ? { ...vertical, right: window.innerWidth - trigger.right }
        : { ...vertical, left: trigger.left },
    );
  }, [open, align]);

  useEffect(() => {
    if (!open) return;
    function onPointer(e: MouseEvent) {
      const target = e.target as Node;
      if (
        rootRef.current?.contains(target) ||
        menuRef.current?.contains(target)
      )
        return;
      setOpen(false);
    }
    function onKey(e: KeyboardEvent) {
      if (e.key === "Escape") setOpen(false);
    }
    // The menu is fixed to where the trigger was; once anything scrolls or the
    // window resizes it would drift, so close it instead.
    function onMove() {
      setOpen(false);
    }
    document.addEventListener("mousedown", onPointer);
    document.addEventListener("keydown", onKey);
    window.addEventListener("scroll", onMove, true);
    window.addEventListener("resize", onMove);
    return () => {
      document.removeEventListener("mousedown", onPointer);
      document.removeEventListener("keydown", onKey);
      window.removeEventListener("scroll", onMove, true);
      window.removeEventListener("resize", onMove);
    };
  }, [open]);

  return (
    <div ref={rootRef} className="relative">
      <button
        ref={buttonRef}
        type="button"
        aria-haspopup="menu"
        aria-expanded={open}
        aria-label={label}
        onClick={() => setOpen((o) => !o)}
        className={triggerClassName}
      >
        {trigger}
      </button>
      {open &&
        createPortal(
          <div
            ref={menuRef}
            role="menu"
            style={position}
            className="fixed z-50 min-w-[10rem] border border-neutral-700 bg-neutral-900 py-1 shadow-md"
          >
            {items.map((item) => (
              <button
                key={item.label}
                type="button"
                role="menuitem"
                disabled={item.disabled}
                onClick={() => {
                  setOpen(false);
                  item.onSelect();
                }}
                className={`flex w-full flex-col items-start px-3 py-1.5 text-left text-sm hover:bg-neutral-800 disabled:cursor-not-allowed disabled:hover:bg-transparent ${
                  item.danger ? "text-red-300" : "text-neutral-300"
                }`}
              >
                <span
                  className={`flex items-center gap-2 ${item.disabled ? "opacity-40" : ""}`}
                >
                  {item.icon}
                  {item.label}
                </span>
                {item.hint && (
                  <span className="max-w-[14rem] text-xs text-neutral-500">
                    {item.hint}
                  </span>
                )}
              </button>
            ))}
          </div>,
          document.body,
        )}
    </div>
  );
}
