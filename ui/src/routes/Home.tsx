import { useOrg } from "../contexts/OrgContext";

// Home is the landing page once an organization is selected. It's intentionally
// minimal — a starting point for your real dashboard.
export function Home() {
  const { currentOrg, currentRole } = useOrg();

  return (
    <div>
      <h1 className="text-2xl font-bold tracking-tight">
        {currentOrg?.name ?? "No organization"}
      </h1>
      <p className="mt-1 text-sm text-gray-600">
        You're working in{" "}
        <span className="font-medium">{currentOrg?.name ?? "—"}</span>
        {currentRole && <> as {currentRole}</>}. Switch organizations from the
        menu in the top bar.
      </p>

      <div className="mt-6 border border-gray-200 bg-white p-6 text-sm text-gray-500">
        Your organization's content goes here.
      </div>
    </div>
  );
}
