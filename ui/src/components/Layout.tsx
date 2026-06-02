import { useEffect, useRef, useState } from "react";
import { Link, Outlet } from "react-router";
import { useAuth } from "react-oidc-context";
import { Check, ChevronDown, Plus } from "lucide-react";
import icon from "@/assets/spacefleet-icon.svg";
import { useOrg } from "../contexts/OrgContext";
import { orgCreationEnabled } from "../lib/appConfig";
import { Sidebar } from "./Sidebar";

export function Layout() {
  const auth = useAuth();
  const email = auth.user?.profile.email;

  return (
    <div className="flex h-screen flex-col bg-gray-50">
      <header className="flex h-14 shrink-0 items-center gap-3 bg-black px-4 text-white">
        <Link to="/" className="flex items-center gap-2" aria-label="Home">
          <img src={icon} alt="Spacefleet" className="h-7 w-7 invert" />
          <span className="text-sm font-semibold tracking-tight">Spacefleet</span>
        </Link>

        <span className="text-white/30">/</span>
        <OrgSwitcher />

        <div className="ml-auto flex items-center gap-3 text-sm">
          {email && <span className="text-gray-300">{email}</span>}
          {/* Local sign-out: clear the stored tokens. Dex doesn't advertise
              an end_session_endpoint, so we don't RP-initiate logout — after
              removeUser the session is gone and AuthGate routes to the /login
              screen. (The identity provider's own session may still be live, so
              this is a local sign-out; /login says as much.) */}
          <button
            type="button"
            onClick={() => void auth.removeUser()}
            className="border border-white/30 px-3 py-1 font-medium text-white hover:bg-white/10"
          >
            Sign out
          </button>
        </div>
      </header>

      <div className="flex flex-1 overflow-hidden">
        <Sidebar />
        <main className="flex-1 overflow-auto p-8">
          <Outlet />
        </main>
      </div>
    </div>
  );
}

// OrgSwitcher shows the current organization and, on click, a dropdown to
// switch between the organizations the user belongs to or create another.
function OrgSwitcher() {
  const { memberships, currentOrg, setCurrentOrg } = useOrg();
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

  return (
    <div ref={ref} className="relative">
      <button
        type="button"
        onClick={() => setOpen((v) => !v)}
        className="flex items-center gap-1.5 px-2 py-1 text-sm font-medium text-white hover:bg-white/10"
        aria-haspopup="menu"
        aria-expanded={open}
      >
        <span className="max-w-[12rem] truncate">
          {currentOrg?.name ?? "Select organization"}
        </span>
        <ChevronDown className="h-4 w-4 text-white/60" aria-hidden />
      </button>

      {open && (
        <div
          role="menu"
          className="absolute left-0 top-full z-20 mt-1 w-64 border border-gray-200 bg-white py-1 text-gray-900 shadow-lg"
        >
          <p className="px-3 py-1 text-xs font-medium uppercase tracking-wide text-gray-400">
            Organizations
          </p>
          {memberships.map((m) => {
            const isCurrent = m.organization.id === currentOrg?.id;
            return (
              <button
                key={m.organization.id}
                type="button"
                role="menuitem"
                onClick={() => {
                  setCurrentOrg(m.organization.id);
                  setOpen(false);
                }}
                className="flex w-full items-center gap-2 px-3 py-2 text-left text-sm hover:bg-gray-100"
              >
                <Check
                  className={`h-4 w-4 shrink-0 ${isCurrent ? "text-black" : "text-transparent"}`}
                  aria-hidden
                />
                <span className="truncate">{m.organization.name}</span>
                <span className="ml-auto text-xs text-gray-400">{m.role}</span>
              </button>
            );
          })}
          {orgCreationEnabled() && (
            <>
              <div className="my-1 border-t border-gray-100" />
              <Link
                to="/organizations/new"
                role="menuitem"
                onClick={() => setOpen(false)}
                className="flex w-full items-center gap-2 px-3 py-2 text-left text-sm hover:bg-gray-100"
              >
                <Plus className="h-4 w-4 shrink-0 text-gray-500" aria-hidden />
                Create organization
              </Link>
            </>
          )}
        </div>
      )}
    </div>
  );
}
