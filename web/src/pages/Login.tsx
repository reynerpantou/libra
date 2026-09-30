import { useEffect, useRef, useState } from "react";
import { Link, Navigate, useNavigate, useSearchParams } from "react-router-dom";
import { api, asset, BASE } from "../lib/api";
import { useAuth } from "../lib/auth";
import ProviderIcon from "../components/ProviderIcon";

const ERRORS: Record<string, string> = {
  cancelled: "Sign-in was cancelled.",
  expired: "That sign-in took too long. Try again.",
  failed: "Sign-in failed. Try again.",
  unavailable: "That sign-in option isn't available.",
  not_invited: "There's no Libra account for that email.",
  rate_limited: "Too many attempts. Wait a minute and try again.",
  setup_done: "Libra already has an owner. Sign in normally, or ask the owner for an invitation.",
};

export function Brand() {
  return (
    <div className="brand" style={{ padding: 0, fontSize: 20 }}>
      <img src={asset("icon.svg")} alt="" style={{ width: 32, height: 32 }} />
      Libra
    </div>
  );
}

export default function Login() {
  const { user, loading } = useAuth();
  const [params] = useSearchParams();
  const [providers, setProviders] = useState<string[] | null>(null);
  const [setupNeeded, setSetupNeeded] = useState(false);
  useEffect(() => {
    api
      .providers()
      .then((r) => {
        setProviders(r.providers);
        setSetupNeeded(r.setup_needed);
      })
      .catch(() => setProviders([]));
  }, []);
  if (!loading && user) return <Navigate to="/" replace />;
  const code = params.get("error");
  const email = params.get("email");
  return (
    <div className="login-wrap">
      <div className="card login-card">
        <Brand />
        <p className="muted">Experimentation platform: design, run and read A/B tests with your own business metrics.</p>
        {code && (
          <div className="alert alert-bad">
            {ERRORS[code] ?? ERRORS.failed}
            {code === "not_invited" && email && <div className="small">Signed in as {email}. Ask an admin to invite this exact address.</div>}
          </div>
        )}
        {setupNeeded && (
          <div className="alert alert-warn small">
            Libra hasn't been set up yet. Open the owner setup link printed in the server's log (or run <code>libra setup-link</code> on the server).
          </div>
        )}
        {providers === null ? (
          <p className="faint">Loading…</p>
        ) : providers.length === 0 ? (
          <div className="alert alert-info small">
            No sign-in option is set up yet. Add <code>LIBRA_GOOGLE_CLIENT_ID</code> and <code>LIBRA_GOOGLE_CLIENT_SECRET</code> to <code>.env</code> and
            restart, or run <code>libra sign-in-link &lt;username&gt;</code> on the server.
          </div>
        ) : (
          <div className="stack-sm">
            {providers.map((p) => (
              // A real navigation, not fetch: the browser has to visit the provider's page.
              <a key={p} className={`btn provider-btn provider-${p}`} href={`${BASE}/api/auth/${p}/start`}>
                <ProviderIcon provider={p} />
                Continue with {p === "google" ? "Google" : "Apple"}
              </a>
            ))}
          </div>
        )}
        <p className="faint small">Libra is invite-only. An admin adds your email first.</p>
      </div>
    </div>
  );
}

// SignInLink redeems a one-time link made with `libra sign-in-link`. The
// token is in the URL fragment, which browsers never send to servers, so
// link previews can't use it up.
export function SignInLink() {
  const { refresh } = useAuth();
  const nav = useNavigate();
  const [failed, setFailed] = useState(false);
  const started = useRef(false);
  useEffect(() => {
    if (started.current) return;
    started.current = true;
    const token = window.location.hash.slice(1);
    window.history.replaceState(null, "", window.location.pathname);
    if (!token) {
      setFailed(true);
      return;
    }
    api
      .redeemLink(token)
      .then(async () => {
        await refresh();
        nav("/", { replace: true });
      })
      .catch(() => setFailed(true));
  }, [nav, refresh]);
  return (
    <div className="login-wrap">
      <div className="card login-card">
        <Brand />
        {failed ? (
          <>
            <div className="alert alert-bad">This sign-in link is invalid or has expired.</div>
            <Link className="btn" to="/login">
              Back to sign-in
            </Link>
          </>
        ) : (
          <p className="muted">Signing you in…</p>
        )}
      </div>
    </div>
  );
}
