import { useEffect, useRef, useState } from "react";
import { useNavigate } from "react-router-dom";
import { api } from "../lib/api";
import { ago } from "../lib/format";
import type { Notification } from "../lib/types";
import { relative } from "./Schedule";
import { Icon, Popover } from "./ui";

const verb: Record<string, string> = {
  create: "created",
  clone: "created (as a clone)",
  submit: "submitted for review",
  withdraw: "withdrew the review of",
  approve: "approved",
  reject: "rejected",
  start: "started",
  pause: "paused",
  resume: "resumed",
  stop: "stopped",
  launch: "launched",
  archive: "archived",
};

const icon: Record<string, string> = {
  start_due: "⏰",
  ending_soon: "⏳",
  auto_stop: "■",
  review_requested: "✎",
  approve: "✓",
  reject: "✗",
  launch: "🚀",
  tuning_finished: "★",
};

// message phrases a notification for its reader.
export function message(n: Notification): { title: string; body?: string; tone?: "warn" | "good" | "bad" } {
  const name = `“${n.experiment || "an experiment"}”`;
  const who = n.actor || "Libra";
  const d = n.detail ?? {};
  switch (n.kind) {
    case "review_requested":
      return { title: `${who} asked you to review ${name}`, body: "Open it to approve or reject." };
    case "reject":
      return { title: `${who} rejected ${name}`, body: typeof d.note === "string" && d.note ? `“${d.note}”` : undefined, tone: "bad" };
    case "approve":
      return { title: `${who} approved ${name}`, body: "It's ready to start.", tone: "good" };
    case "start_due":
      return d.status === "approved"
        ? { title: `Time to start ${name}`, body: `It was planned to start ${relative(String(d.planned_start))} and is approved.`, tone: "warn" }
        : { title: `${name} was planned to start ${relative(String(d.planned_start))}`, body: `It's still ${String(d.status).replace("_", " ")} — it needs approval before it can start.`, tone: "warn" };
    case "ending_soon":
      return {
        title: `${name} ends ${relative(String(d.end_at))}`,
        body: n.experiment_kind === "tuning" ? "Its last round is running. Add rounds to keep searching." : "It stops then unless you extend it.",
        tone: "warn",
      };
    case "auto_stop":
      return { title: `${name} reached its end date and stopped`, body: "The report is frozen; launch a variant or archive it." };
    case "tuning_finished":
      return { title: `${name} finished all its rounds`, body: typeof d.note === "string" ? d.note : "Launch the recommendation.", tone: "good" };
    case "extend":
      return {
        title: `${who} extended ${name}`,
        body: d.rounds ? `+${d.rounds} rounds (${d.max_rounds} in total)` : d.end_at === "none" ? "It no longer has an end date." : `It now ends ${relative(String(d.end_at))}.`,
      };
    default:
      return { title: `${who} ${verb[n.kind] ?? n.kind} ${name}` };
  }
}

const link = (n: Notification) => (n.experiment_id ? (n.experiment_kind === "tuning" ? `/tuning/${n.experiment_id}` : `/experiments/${n.experiment_id}`) : "/");

// Bell shows the unread count and opens the notification panel.
export function Bell() {
  const [unread, setUnread] = useState(0);
  const [open, setOpen] = useState(false);
  const btn = useRef<HTMLButtonElement>(null);
  useEffect(() => {
    let alive = true;
    const poll = () =>
      api
        .unreadNotifications()
        .then((r) => alive && setUnread(r.unread))
        .catch(() => {});
    poll();
    const t = setInterval(poll, 60000);
    return () => {
      alive = false;
      clearInterval(t);
    };
  }, []);
  return (
    <>
      <button ref={btn} className="icon-btn bell" aria-label={`Notifications${unread ? ` (${unread} unread)` : ""}`} onClick={() => setOpen(!open)}>
        <Icon name="bell" size={19} />
        {unread > 0 && <span className="bell-count">{unread > 99 ? "99+" : unread}</span>}
      </button>
      {open && btn.current && (
        <Popover anchor={btn.current} onClose={() => setOpen(false)} width={400}>
          <Panel onUnread={setUnread} onClose={() => setOpen(false)} />
        </Popover>
      )}
    </>
  );
}

function Panel({ onUnread, onClose }: { onUnread: (n: number) => void; onClose: () => void }) {
  const nav = useNavigate();
  const [items, setItems] = useState<Notification[]>([]);
  const [onlyUnread, setOnlyUnread] = useState(false);
  const [more, setMore] = useState(true);
  const [loading, setLoading] = useState(false);
  const load = async (reset: boolean) => {
    setLoading(true);
    try {
      const before = !reset && items.length ? String(items[items.length - 1].id) : undefined;
      const r = await api.notifications({ before, unread: onlyUnread ? "1" : undefined });
      setItems(reset ? r.items : [...items, ...r.items]);
      setMore(r.items.length === 20);
      onUnread(r.unread);
    } finally {
      setLoading(false);
    }
  };
  useEffect(() => {
    load(true);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [onlyUnread]);
  const open = async (n: Notification) => {
    if (!n.read) {
      await api.readNotifications({ ids: [n.id] }).catch(() => {});
      setItems(items.map((x) => (x.id === n.id ? { ...x, read: true } : x)));
    }
    onClose();
    nav(link(n));
  };
  return (
    <div className="notif-panel">
      <div className="row-between notif-head">
        <b>Notifications</b>
        <div className="row" style={{ gap: 8 }}>
          <label className="check small">
            <input type="checkbox" checked={onlyUnread} onChange={(e) => setOnlyUnread(e.target.checked)} /> Unread only
          </label>
          <button
            className="link-btn small"
            onClick={async () => {
              await api.readNotifications({ all: true });
              setItems(items.map((x) => ({ ...x, read: true })));
              onUnread(0);
            }}
          >
            Mark all read
          </button>
        </div>
      </div>
      <div className="notif-list">
        {items.length === 0 && !loading && <div className="small faint" style={{ padding: 16 }}>Nothing here yet. You'll hear about experiments you own and reviews you're asked for.</div>}
        {items.map((n) => {
          const m = message(n);
          return (
            <button key={n.id} className={`notif ${n.read ? "" : "unread"} ${m.tone ? `notif-${m.tone}` : ""}`} onClick={() => open(n)}>
              <span className="notif-icon">{icon[n.kind] ?? "•"}</span>
              <span className="notif-text">
                <span className="notif-title">{m.title}</span>
                {m.body && <span className="notif-body">{m.body}</span>}
                <span className="notif-time">
                  {ago(n.created_at)}
                  {n.experiment_kind === "tuning" ? " · tuning study" : ""}
                </span>
              </span>
              {!n.read && <span className="notif-dot" aria-label="unread" />}
            </button>
          );
        })}
        {more && items.length > 0 && (
          <button className="btn btn-sm btn-ghost" style={{ margin: 8 }} disabled={loading} onClick={() => load(false)}>
            {loading ? "Loading…" : "Older"}
          </button>
        )}
      </div>
    </div>
  );
}
