import { MoreVertical } from "lucide-react";
import { Dropdown, type DropdownItem } from "./Dropdown";

// ActionsMenu is the standard overflow menu for an object's secondary actions
// (edit, delete, move, …): a vertical-ellipsis button — bordered and sized like
// the page's other buttons, so it reads as one — that opens a Dropdown of items. Use it wherever an object's actions would otherwise be a
// row of buttons. `label` is the trigger's accessible name — conventionally
// "<object> actions". Clicks inside it (the trigger and the portaled menu,
// which bubble through the React tree) stop here, so it can sit inside a
// clickable row or card without also firing that row's handler.
export function ActionsMenu({
  label,
  items,
  align = "right",
}: {
  label: string;
  items: DropdownItem[];
  align?: "left" | "right";
}) {
  return (
    <div onClick={(e) => e.stopPropagation()}>
      <Dropdown
        label={label}
        items={items}
        align={align}
        triggerClassName="inline-flex items-center justify-center border border-neutral-700 p-2 text-neutral-300 hover:bg-neutral-800"
        trigger={<MoreVertical className="h-4 w-4" />}
      />
    </div>
  );
}
