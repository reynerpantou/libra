import { Link } from "react-router-dom";

// AB Tuning is planned: a placeholder so the navigation has its home.
export default function Tuning() {
  return (
    <div className="page" style={{ maxWidth: 760 }}>
      <div className="page-head">
        <div>
          <h1>AB Tuning</h1>
          <p>Coming soon.</p>
        </div>
      </div>
      <section className="card card-pad stack">
        <p className="muted">
          AB Tuning will search for the best value of a parameter (a ranking weight, a threshold, a page size) instead of comparing a few fixed variants.
          It will reuse what's already here: <Link to="/layers">traffic layers</Link>, <Link to="/diversions">diversions</Link>,{" "}
          <Link to="/attributes">targeting</Link> and your <Link to="/businesses">business metrics</Link>.
        </p>
        <p className="muted small">
          Until then, run an <Link to="/experiments/new">AB test</Link> with one variant per candidate value.
        </p>
      </section>
    </div>
  );
}
