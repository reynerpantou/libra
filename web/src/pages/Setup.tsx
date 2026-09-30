import { useEffect, useState } from "react";
import ProviderIcon from "../components/ProviderIcon";
import { api, ApiError, BASE } from "../lib/api";
import { Brand } from "./Login";

// Claiming the owner account with the one-time link the server printed to
// its log. The token stays in the #fragment (never sent anywhere by the
// browser) and goes to the server only in the request that starts sign-in.
export default function Setup() {
  const [token] = useState(() => window.location.hash.slice(1));
  const [providers, setProviders] = useState<string[] | null>(null);
  const [needed, setNeeded] = useState(true);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState("");

  useEffect(() => {
    // Keep the token out of history and screenshots once it's read.
    window.history.replaceState(null, "", window.location.pathname);
    api
      .providers()
      .then((r) => {
        setProviders(r.providers);
        setNeeded(r.setup_needed);
      })
      .catch(() => setProviders([]));
  }, []);

  const start = async (provider: string) => {
    setError("");
    setBusy(provider);
    try {
      const { redirect } = await api.setupStart(token, provider);
      window.location.assign(redirect);
    } catch (e) {
      const code = e instanceof ApiError ? e.code : "";
      setError(
        code === "setup_done"
          ? "Libra already has an owner, so this setup link no longer works."
          : code === "setup_link_invalid"
          ? "This setup link is invalid or has expired. Run `libra setup-link` on the server for a new one."
          : "Couldn't start sign-in. Please try again."
      );
      setBusy("");
    }
  };

  return (
    <div className="login-wrap">
      <div className="card login-card">
        <Brand />
        <h2>Claim Libra</h2>
        {!needed ? (
          <>
            <p className="muted">Libra already has an owner, so this setup link no longer works.</p>
            <a className="btn" href={`${BASE}/login`}>
              Back to sign-in
            </a>
          </>
        ) : !token ? (
          <div className="alert alert-bad">This page needs the full setup link from the server's log, including the part after #.</div>
        ) : (
          <>
            <p className="muted">
              Sign in with the Google or Apple account that should own this Libra. It becomes the admin with full control, and nobody can take that
              away from inside the app.
            </p>
            {error && <div className="alert alert-bad">{error}</div>}
            {providers === null ? (
              <p className="faint">Loading…</p>
            ) : providers.length === 0 ? (
              <div className="alert alert-info small">
                No sign-in option is configured on this server yet. Add <code>LIBRA_GOOGLE_CLIENT_ID</code> and <code>LIBRA_GOOGLE_CLIENT_SECRET</code> to{" "}
                <code>.env</code>, restart Libra, then open the new setup link from its log.
              </div>
            ) : (
              <div className="stack-sm">
                {providers.map((p) => (
                  <button key={p} type="button" className={`btn provider-btn provider-${p}`} disabled={!!busy} onClick={() => void start(p)}>
                    <ProviderIcon provider={p} />
                    Continue with {p === "google" ? "Google" : "Apple"}
                  </button>
                ))}
              </div>
            )}
            <p className="faint small">The link works once. After that, everyone else joins by invitation.</p>
          </>
        )}
      </div>
    </div>
  );
}
