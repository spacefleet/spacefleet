import type { ReactNode } from "react";

// StickyFooter is a full-page form's action bar — Cancel and Save, right-
// aligned, with an optional status or error note on the left — on a raised
// neutral-800 panel that sets it apart from both the page and the form's
// neutral-900 card (so a secondary button inside hovers to neutral-700). It
// pins to the bottom of the app shell's scrolling page while the form runs
// past the fold, and settles in place at the form's end once scrolled there.
// It must be the last child of its parent (sticky stays inside its parent's
// box). Sticky insets are measured inside the scroll container's padding, so
// -bottom-8 cancels Layout's p-8 on <main> to pin the bar flush to the
// window's bottom (with bottom-0 the form shows through the padding below
// it). Popovers in the form (z-20) still open over it.
export function StickyFooter({
  note,
  children,
}: {
  note?: ReactNode;
  children: ReactNode;
}) {
  return (
    <div className="sticky -bottom-8 z-10 mt-6 flex items-center justify-end gap-3 border border-neutral-700 bg-neutral-800 px-4 py-3">
      {note && <div className="mr-auto min-w-0">{note}</div>}
      {children}
    </div>
  );
}
