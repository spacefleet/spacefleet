import { useEffect, useMemo, useState } from "react";
import { GitBranch, Lock, Search, X } from "lucide-react";
import { api } from "../../api/client";
import type { components } from "../../api/schema";

type GitHubRepository = components["schemas"]["GitHubRepository"];

// RepositoryPicker is a "Select repository" button that opens a searchable modal
// listing every repository reachable across the organization's GitHub App
// installations (GET /api/github/repositories). Picking one calls onSelect with
// the repo — the caller fills the clone URL and selects the matching
// installation. Repos are fetched lazily when the modal opens, not on mount, so
// a component with no git source never pays for the call.
export function RepositoryPicker({
  onSelect,
  disabled = false,
  label = "Select repository",
}: {
  onSelect: (repo: GitHubRepository) => void;
  disabled?: boolean;
  label?: string;
}) {
  const [open, setOpen] = useState(false);
  return (
    <>
      <button
        type="button"
        onClick={() => setOpen(true)}
        disabled={disabled}
        className="inline-flex items-center gap-1.5 border border-neutral-300 px-2.5 py-1.5 text-xs font-medium text-neutral-700 hover:bg-neutral-50 disabled:cursor-not-allowed disabled:opacity-50"
      >
        <GitBranch className="h-3.5 w-3.5" />
        {label}
      </button>
      {open && (
        <RepositoryPickerModal
          onClose={() => setOpen(false)}
          onSelect={(repo) => {
            onSelect(repo);
            setOpen(false);
          }}
        />
      )}
    </>
  );
}

function RepositoryPickerModal({
  onClose,
  onSelect,
}: {
  onClose: () => void;
  onSelect: (repo: GitHubRepository) => void;
}) {
  const [repos, setRepos] = useState<GitHubRepository[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [query, setQuery] = useState("");

  useEffect(() => {
    let cancelled = false;
    (async () => {
      const { data, error } = await api.GET("/api/github/repositories");
      if (cancelled) return;
      if (error) setError(error.message ?? "Could not load repositories");
      setRepos(data ?? []);
      setLoading(false);
    })();
    return () => {
      cancelled = true;
    };
  }, []);

  // Close on Escape, matching the rest of the app's modals.
  useEffect(() => {
    function onKey(e: KeyboardEvent) {
      if (e.key === "Escape") onClose();
    }
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onClose]);

  // Filter by full name, then group by the installation account so the list
  // reads "account › repos" like the connect page.
  const groups = useMemo(() => {
    const q = query.trim().toLowerCase();
    const filtered = q
      ? repos.filter((r) => r.full_name.toLowerCase().includes(q))
      : repos;
    const byAccount = new Map<string, GitHubRepository[]>();
    for (const r of filtered) {
      const key = r.account_login || "(unknown account)";
      const list = byAccount.get(key);
      if (list) list.push(r);
      else byAccount.set(key, [r]);
    }
    return [...byAccount.entries()].sort(([a], [b]) => a.localeCompare(b));
  }, [repos, query]);

  return (
    <div
      className="fixed inset-0 z-50 flex items-start justify-center overflow-y-auto bg-black/40 p-4"
      onClick={onClose}
    >
      <div
        className="mt-12 flex max-h-[70vh] w-full max-w-lg flex-col border border-neutral-200 bg-white shadow-lg"
        onClick={(e) => e.stopPropagation()}
      >
        <div className="flex items-center justify-between border-b border-neutral-200 px-5 py-3">
          <h2 className="inline-flex items-center gap-2 text-lg font-semibold tracking-tight">
            <GitBranch className="h-5 w-5 text-neutral-500" />
            Select a repository
          </h2>
          <button
            type="button"
            onClick={onClose}
            className="text-neutral-400 hover:text-neutral-700"
            aria-label="Close"
          >
            <X className="h-5 w-5" />
          </button>
        </div>

        <div className="border-b border-neutral-200 px-5 py-3">
          <div className="flex items-center gap-2 border border-neutral-300 px-2">
            <Search className="h-4 w-4 text-neutral-400" />
            <input
              className="w-full py-2 text-sm outline-none"
              placeholder="Search repositories…"
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              autoFocus
            />
          </div>
        </div>

        <div className="flex-1 overflow-y-auto px-5 py-3">
          {loading ? (
            <p className="text-sm text-neutral-500">Loading repositories…</p>
          ) : error ? (
            <p className="text-sm text-red-600">{error}</p>
          ) : repos.length === 0 ? (
            <p className="text-sm text-neutral-500">
              No repositories found. Make sure the GitHub App is installed on the
              repositories you want, then try again.
            </p>
          ) : groups.length === 0 ? (
            <p className="text-sm text-neutral-500">
              No repositories match “{query}”.
            </p>
          ) : (
            <div className="space-y-4">
              {groups.map(([account, accountRepos]) => (
                <div key={account}>
                  <p className="mb-1 text-xs font-medium uppercase tracking-wide text-neutral-500">
                    {account}
                  </p>
                  <ul>
                    {accountRepos.map((repo) => (
                      <li key={`${repo.installation_id}:${repo.full_name}`}>
                        <button
                          type="button"
                          onClick={() => onSelect(repo)}
                          className="flex w-full items-center gap-2 px-2 py-1.5 text-left text-sm text-neutral-800 hover:bg-neutral-100"
                        >
                          {repo.private && (
                            <Lock className="h-3.5 w-3.5 shrink-0 text-neutral-400" />
                          )}
                          <span className="truncate">{repo.full_name}</span>
                        </button>
                      </li>
                    ))}
                  </ul>
                </div>
              ))}
            </div>
          )}
        </div>
      </div>
    </div>
  );
}
