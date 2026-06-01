import { useAuth } from "react-oidc-context";

// NoOrganizations is shown to a signed-in user who belongs to no organization
// when this server has organization creation disabled (the ALLOW_ORG_CREATION
// security setting). There is nothing for them to do but ask an existing
// member to invite them, so we say exactly that — no create form. It mirrors
// the standalone, sidebar-less shell of the create-organization screen since
// the user is still outside any org.
export function NoOrganizations() {
  const auth = useAuth();

  return (
    <div className="flex min-h-screen flex-col bg-neutral-50">
      <header className="flex h-14 shrink-0 items-center bg-black px-4 text-sm text-white">
        <span className="font-semibold tracking-tight">Spacefleet</span>
        <div className="ml-auto flex items-center gap-3">
          {auth.user?.profile.email && (
            <span className="text-neutral-300">{auth.user.profile.email}</span>
          )}
          <button
            type="button"
            onClick={() => void auth.removeUser()}
            className="border border-white/30 px-3 py-1 font-medium text-white hover:bg-white/10"
          >
            Sign out
          </button>
        </div>
      </header>

      <main className="flex flex-1 items-center justify-center p-8">
        <div className="w-full max-w-md border border-neutral-200 bg-white p-6">
          <h1 className="text-2xl font-bold tracking-tight">
            You're not in an organization yet
          </h1>
          <p className="mt-2 text-sm text-neutral-600">
            You don't belong to any organizations on Spacefleet, and this server
            doesn't allow creating new ones. Ask an existing member to invite you
            to their organization, then sign in again.
          </p>
        </div>
      </main>
    </div>
  );
}
