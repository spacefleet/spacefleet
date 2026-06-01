import { useLocation } from "react-router";
import { navLeaves } from "../nav";

// Placeholder is the stand-in page rendered for every scaffolded nav leaf that
// doesn't yet have a real implementation. It reads the nav config to show the
// section breadcrumb and page title, so each route is visibly "wired" while you
// build the actual pages. To implement a page for real, swap its <Placeholder />
// in App.tsx for a dedicated component.
export function Placeholder() {
  const { pathname } = useLocation();
  const match = navLeaves.find(({ leaf }) => leaf.path === pathname);
  const sectionLabel = match?.section.label ?? "";
  const title = match?.leaf.label ?? "Page";

  return (
    <div>
      {sectionLabel && (
        <p className="text-xs font-medium uppercase tracking-wide text-gray-400">
          {sectionLabel}
        </p>
      )}
      <h1 className="mt-1 text-2xl font-bold tracking-tight">{title}</h1>
      <div className="mt-6 border border-gray-200 bg-white p-6 text-sm text-gray-500">
        {sectionLabel ? `${sectionLabel} › ${title}` : title} — placeholder.
        Build this page out here.
      </div>
    </div>
  );
}
