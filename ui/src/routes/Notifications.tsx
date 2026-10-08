import { useCallback, useEffect, useState, type FormEvent } from "react";
import { Bell, KeyRound, Plus, Send, Trash2, X } from "lucide-react";
import { api } from "../api/client";
import { useOrg } from "../contexts/OrgContext";
import type { components } from "../api/schema";

type Channel = components["schemas"]["NotificationChannel"];
type Kind = components["schemas"]["NotificationChannelKind"];
type EventKind = components["schemas"]["RunEventKind"];
type Application = components["schemas"]["Application"];

const KIND_LABELS: Record<Kind, string> = {
  email: "Email",
  slack: "Slack",
  webhook: "Webhook",
};

const EVENTS: { kind: EventKind; label: string; hint: string }[] = [
  { kind: "awaiting_approval", label: "Awaiting approval", hint: "a run parked at an approval gate" },
  { kind: "run_failed", label: "Run failed", hint: "a run that failed, timed out at a gate, or was abandoned" },
  { kind: "drift_detected", label: "Drift detected", hint: "a drift check that found changes made outside of OpenTofu" },
];

const EVENT_LABEL: Record<EventKind, string> = Object.fromEntries(
  EVENTS.map((e) => [e.kind, e.label]),
) as Record<EventKind, string>;

// Notifications is the Admin › Notifications page: where run events go —
// an email address, a Slack incoming webhook, or a generic webhook — with
// the events each receives and an optional single application. Webhook
// URLs are sealed server-side and never returned (only their host shows).
// Admins manage channels; other members can see them.
export function Notifications() {
  const { currentOrg, currentRole } = useOrg();
  const isAdmin = currentRole === "admin";
  const [channels, setChannels] = useState<Channel[]>([]);
  const [apps, setApps] = useState<Application[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [adding, setAdding] = useState(false);
  const [signing, setSigning] = useState<Channel | null>(null);
  const [notice, setNotice] = useState<string | null>(null);

  const load = useCallback(async () => {
    setLoading(true);
    setError(null);
    const [{ data, error }, appsRes] = await Promise.all([
      api.GET("/api/notification-channels"),
      api.GET("/api/applications"),
    ]);
    if (error) setError(error.message ?? "Could not load notification channels");
    setChannels(data ?? []);
    setApps(appsRes.data ?? []);
    setLoading(false);
  }, []);

  useEffect(() => {
    void load();
  }, [load, currentOrg?.id]);

  const appName = (id?: string | null) =>
    id ? (apps.find((a) => a.id === id)?.name ?? "one application") : "all applications";

  async function onDelete(ch: Channel) {
    if (!confirm(`Delete the channel "${ch.name}"?`)) return;
    const { error } = await api.DELETE("/api/notification-channels/{id}", {
      params: { path: { id: ch.id } },
    });
    if (error) {
      setError(error.message ?? "Could not delete the channel");
      return;
    }
    setChannels((cs) => cs.filter((x) => x.id !== ch.id));
  }

  async function onTest(ch: Channel) {
    setNotice(null);
    setError(null);
    const { error } = await api.POST("/api/notification-channels/{id}/test", {
      params: { path: { id: ch.id } },
    });
    if (error) {
      setError(error.message ?? "Could not send a test notification");
      return;
    }
    setNotice(`Test notification sent to ${ch.name}.`);
  }

  return (
    <div>
      <div className="flex items-start justify-between">
        <div>
          <p className="text-xs font-medium uppercase tracking-wide text-neutral-400">
            Admin
          </p>
          <h1 className="mt-1 text-2xl font-bold tracking-tight">Notifications</h1>
          <p className="mt-1 text-sm text-neutral-600">
            Where run events go: a run waiting for approval, a failed run, or
            drift found by a check — by email, to Slack, or to any webhook.
          </p>
        </div>
        {isAdmin && (
          <button
            type="button"
            onClick={() => setAdding(true)}
            className="inline-flex items-center gap-2 bg-black px-4 py-2 text-sm font-medium text-white hover:bg-neutral-800"
          >
            <Plus className="h-4 w-4" />
            Add channel
          </button>
        )}
      </div>

      {notice && <p className="mt-4 text-sm text-emerald-700">{notice}</p>}

      <div className="mt-6 border border-neutral-200 bg-white">
        {loading ? (
          <p className="p-6 text-sm text-neutral-500">Loading…</p>
        ) : error ? (
          <p className="p-6 text-sm text-red-600">{error}</p>
        ) : channels.length === 0 ? (
          <div className="p-10 text-center">
            <Bell className="mx-auto h-8 w-8 text-neutral-300" />
            <p className="mt-3 text-sm font-medium text-neutral-700">No channels yet</p>
            <p className="mt-1 text-sm text-neutral-500">
              Add one to be told when a run needs approval, fails, or finds drift.
            </p>
          </div>
        ) : (
          <table className="w-full text-sm">
            <thead>
              <tr className="border-b border-neutral-200 text-left text-xs uppercase tracking-wide text-neutral-400">
                <th className="px-4 py-2 font-medium">Name</th>
                <th className="px-4 py-2 font-medium">Kind</th>
                <th className="px-4 py-2 font-medium">Destination</th>
                <th className="px-4 py-2 font-medium">Events</th>
                <th className="px-4 py-2 font-medium">Scope</th>
                {isAdmin && <th className="px-4 py-2" />}
              </tr>
            </thead>
            <tbody>
              {channels.map((ch) => (
                <tr key={ch.id} className="border-b border-neutral-100 last:border-0">
                  <td className="px-4 py-3 font-medium text-neutral-900">{ch.name}</td>
                  <td className="px-4 py-3 text-neutral-600">{KIND_LABELS[ch.kind]}</td>
                  <td className="px-4 py-3 font-mono text-xs text-neutral-600">
                    {ch.address}
                    {ch.has_secret && (
                      <span className="ml-2 border border-neutral-300 px-1 py-0.5 font-sans text-[10px] uppercase tracking-wide text-neutral-500">
                        signed
                      </span>
                    )}
                  </td>
                  <td className="px-4 py-3 text-neutral-600">
                    {ch.events.map((e) => EVENT_LABEL[e] ?? e).join(", ")}
                  </td>
                  <td className="px-4 py-3 text-neutral-600">{appName(ch.application_id)}</td>
                  {isAdmin && (
                    <td className="px-4 py-3 text-right">
                      {ch.kind === "webhook" && (
                        <button
                          type="button"
                          onClick={() => setSigning(ch)}
                          title={ch.has_secret ? "Rotate or remove the signing secret" : "Set a signing secret"}
                          aria-label={`Signing secret for ${ch.name}`}
                          className="mr-1 p-1 text-neutral-400 hover:text-neutral-900"
                        >
                          <KeyRound className="h-4 w-4" />
                        </button>
                      )}
                      <button
                        type="button"
                        onClick={() => void onTest(ch)}
                        title="Send a test notification"
                        aria-label={`Test ${ch.name}`}
                        className="mr-1 p-1 text-neutral-400 hover:text-neutral-900"
                      >
                        <Send className="h-4 w-4" />
                      </button>
                      <button
                        type="button"
                        onClick={() => void onDelete(ch)}
                        title="Delete this channel"
                        aria-label={`Delete ${ch.name}`}
                        className="p-1 text-neutral-400 hover:text-red-600"
                      >
                        <Trash2 className="h-4 w-4" />
                      </button>
                    </td>
                  )}
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>

      {signing && (
        <SigningSecretDialog
          channel={signing}
          onClose={() => setSigning(null)}
          onSaved={(ch) => {
            setChannels((cs) => cs.map((x) => (x.id === ch.id ? ch : x)));
            setSigning(null);
            setNotice(
              ch.has_secret
                ? `Deliveries to ${ch.name} are now signed with the new secret.`
                : `Deliveries to ${ch.name} are no longer signed.`,
            );
          }}
        />
      )}
      {adding && (
        <AddChannelDialog
          apps={apps}
          onClose={() => setAdding(false)}
          onCreated={(ch) => {
            setChannels((cs) => [...cs, ch]);
            setAdding(false);
          }}
        />
      )}
    </div>
  );
}

// AddChannelDialog collects a channel: name, kind, destination, events, and
// an optional application. The destination of a webhook kind is sent once
// and sealed server-side.
function AddChannelDialog({
  apps,
  onClose,
  onCreated,
}: {
  apps: Application[];
  onClose: () => void;
  onCreated: (ch: Channel) => void;
}) {
  const [name, setName] = useState("");
  const [kind, setKind] = useState<Kind>("email");
  const [target, setTarget] = useState("");
  const [secret, setSecret] = useState("");
  const [events, setEvents] = useState<EventKind[]>(["awaiting_approval", "run_failed", "drift_detected"]);
  const [appId, setAppId] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const toggle = (e: EventKind) =>
    setEvents((cur) => (cur.includes(e) ? cur.filter((x) => x !== e) : [...cur, e]));

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setSubmitting(true);
    setError(null);
    const { data, error } = await api.POST("/api/notification-channels", {
      body: {
        name: name.trim(),
        kind,
        target: target.trim(),
        events,
        ...(kind === "webhook" && secret.trim() ? { secret: secret.trim() } : {}),
        ...(appId ? { application_id: appId } : {}),
      },
    });
    setSubmitting(false);
    if (error || !data) {
      setError(error?.message ?? "Could not add the channel");
      return;
    }
    onCreated(data);
  };

  const targetLabel =
    kind === "email" ? "Email address" : kind === "slack" ? "Slack incoming webhook URL" : "Webhook URL";
  const targetPlaceholder =
    kind === "email"
      ? "ops@example.com"
      : kind === "slack"
        ? "https://hooks.slack.com/services/…"
        : "https://example.com/hooks/spacefleet";
  const complete = name.trim() !== "" && target.trim() !== "" && events.length > 0;

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/40 p-4">
      <form
        onSubmit={(e) => void submit(e)}
        className="w-full max-w-lg border border-neutral-200 bg-white p-6"
      >
        <div className="flex items-start justify-between">
          <h2 className="text-lg font-semibold">Add notification channel</h2>
          <button type="button" onClick={onClose} aria-label="Close" className="p-1 text-neutral-400 hover:text-neutral-900">
            <X className="h-4 w-4" />
          </button>
        </div>
        <div className="mt-4 grid gap-4">
          <label className="flex flex-col gap-1 text-xs text-neutral-600">
            Name
            <input
              type="text"
              aria-label="Name"
              value={name}
              onChange={(e) => setName(e.target.value)}
              className="border border-neutral-300 px-2 py-1.5 text-sm text-neutral-900"
            />
          </label>
          <label className="flex flex-col gap-1 text-xs text-neutral-600">
            Kind
            <select
              aria-label="Kind"
              value={kind}
              onChange={(e) => {
                setKind(e.target.value as Kind);
                setTarget("");
              }}
              className="border border-neutral-300 bg-white px-2 py-1.5 text-sm text-neutral-900"
            >
              {(Object.keys(KIND_LABELS) as Kind[]).map((k) => (
                <option key={k} value={k}>
                  {KIND_LABELS[k]}
                </option>
              ))}
            </select>
          </label>
          <label className="flex flex-col gap-1 text-xs text-neutral-600">
            {targetLabel}
            <input
              type="text"
              aria-label={targetLabel}
              value={target}
              placeholder={targetPlaceholder}
              onChange={(e) => setTarget(e.target.value)}
              className="border border-neutral-300 px-2 py-1.5 font-mono text-sm text-neutral-900 placeholder:font-sans placeholder:text-neutral-400"
            />
            {kind !== "email" && (
              <span className="text-neutral-500">
                Stored encrypted; only the host is shown afterwards.
              </span>
            )}
            {kind === "email" && (
              <span className="text-neutral-500">
                Needs outbound email configured on this deployment.
              </span>
            )}
          </label>
          {kind === "webhook" && (
            <label className="flex flex-col gap-1 text-xs text-neutral-600">
              Signing secret (optional)
              <input
                type="text"
                aria-label="Signing secret"
                value={secret}
                placeholder="a shared secret your receiver checks"
                onChange={(e) => setSecret(e.target.value)}
                className="border border-neutral-300 px-2 py-1.5 font-mono text-sm text-neutral-900 placeholder:font-sans placeholder:text-neutral-400"
              />
              <span className="text-neutral-500">
                When set, every delivery carries an X-Spacefleet-Signature-256
                header (HMAC-SHA256 of the body) so your receiver can verify it
                came from here. Stored encrypted, never shown again.
              </span>
            </label>
          )}
          <fieldset className="flex flex-col gap-1 text-xs text-neutral-600">
            <legend>Events</legend>
            {EVENTS.map((e) => (
              <label key={e.kind} className="inline-flex items-center gap-2 text-sm text-neutral-700">
                <input
                  type="checkbox"
                  aria-label={e.label}
                  checked={events.includes(e.kind)}
                  onChange={() => toggle(e.kind)}
                  className="h-3.5 w-3.5 accent-black"
                />
                {e.label}
                <span className="text-xs text-neutral-500">— {e.hint}</span>
              </label>
            ))}
          </fieldset>
          <label className="flex flex-col gap-1 text-xs text-neutral-600">
            Application
            <select
              aria-label="Application"
              value={appId}
              onChange={(e) => setAppId(e.target.value)}
              className="border border-neutral-300 bg-white px-2 py-1.5 text-sm text-neutral-900"
            >
              <option value="">All applications</option>
              {apps.map((a) => (
                <option key={a.id} value={a.id}>
                  {a.name}
                </option>
              ))}
            </select>
          </label>
        </div>
        {error && <p className="mt-3 text-sm text-red-600">{error}</p>}
        <div className="mt-5 flex justify-end gap-2">
          <button
            type="button"
            onClick={onClose}
            className="border border-neutral-300 px-3 py-1.5 text-sm text-neutral-700 hover:bg-neutral-50"
          >
            Cancel
          </button>
          <button
            type="submit"
            disabled={!complete || submitting}
            className="bg-black px-3 py-1.5 text-sm font-medium text-white hover:bg-neutral-800 disabled:opacity-50"
          >
            {submitting ? "Saving…" : "Save channel"}
          </button>
        </div>
      </form>
    </div>
  );
}

// SigningSecretDialog sets, rotates, or removes a webhook channel's signing
// secret. The current secret is never shown (it is sealed server-side);
// saving a new one replaces it from the next delivery on, and "Stop
// signing" clears it — the API takes an empty string for that.
function SigningSecretDialog({
  channel,
  onClose,
  onSaved,
}: {
  channel: Channel;
  onClose: () => void;
  onSaved: (ch: Channel) => void;
}) {
  const [secret, setSecret] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const save = async (value: string) => {
    setSubmitting(true);
    setError(null);
    const { data, error } = await api.PATCH("/api/notification-channels/{id}", {
      params: { path: { id: channel.id } },
      body: { secret: value },
    });
    setSubmitting(false);
    if (error || !data) {
      setError(error?.message ?? "Could not update the signing secret");
      return;
    }
    onSaved(data);
  };

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/30 p-4">
      <form
        onSubmit={(e) => {
          e.preventDefault();
          void save(secret.trim());
        }}
        className="w-full max-w-md border border-neutral-200 bg-white p-5 shadow-lg"
      >
        <div className="flex items-start justify-between">
          <h2 className="text-base font-semibold">Signing secret for {channel.name}</h2>
          <button type="button" onClick={onClose} aria-label="Close" className="p-1 text-neutral-400 hover:text-neutral-900">
            <X className="h-4 w-4" />
          </button>
        </div>
        <p className="mt-1 text-sm text-neutral-600">
          {channel.has_secret
            ? "Deliveries are signed. Enter a new secret to rotate it — the current one cannot be shown — or stop signing altogether."
            : "Deliveries are not signed. Set a secret and every delivery will carry an X-Spacefleet-Signature-256 header your receiver can verify."}
        </p>
        <label className="mt-4 flex flex-col gap-1 text-xs text-neutral-600">
          New signing secret
          <input
            type="text"
            aria-label="New signing secret"
            value={secret}
            onChange={(e) => setSecret(e.target.value)}
            placeholder="a shared secret your receiver checks"
            className="border border-neutral-300 px-2 py-1.5 font-mono text-sm text-neutral-900 placeholder:font-sans placeholder:text-neutral-400"
            autoFocus
          />
        </label>
        {error && <p className="mt-3 text-sm text-red-600">{error}</p>}
        <div className="mt-5 flex items-center justify-between gap-2">
          {channel.has_secret ? (
            <button
              type="button"
              onClick={() => void save("")}
              disabled={submitting}
              className="border border-red-300 px-3 py-1.5 text-sm text-red-700 hover:bg-red-50 disabled:opacity-50"
            >
              Stop signing
            </button>
          ) : (
            <span />
          )}
          <div className="flex gap-2">
            <button
              type="button"
              onClick={onClose}
              className="border border-neutral-300 px-3 py-1.5 text-sm text-neutral-700 hover:bg-neutral-50"
            >
              Cancel
            </button>
            <button
              type="submit"
              disabled={submitting || secret.trim() === ""}
              className="bg-black px-3 py-1.5 text-sm font-medium text-white hover:bg-neutral-800 disabled:opacity-50"
            >
              {submitting ? "Saving…" : channel.has_secret ? "Rotate secret" : "Start signing"}
            </button>
          </div>
        </div>
      </form>
    </div>
  );
}
