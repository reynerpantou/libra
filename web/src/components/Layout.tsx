import { useEffect, useState } from "react";
import { NavLink, Outlet, useLocation, useNavigate } from "react-router-dom";
import { asset } from "../lib/api";
import { useAuth, useCan } from "../lib/auth";
import { Icon } from "./ui";

function useTheme(): [string, () => void] {
  const [theme, setTheme] = useState<string>(() => {
    try {
      return localStorage.getItem("libra-theme") ?? "";
    } catch {
      return "";
    }
  });
  useEffect(() => {
    if (theme) document.documentElement.setAttribute("data-theme", theme);
    else document.documentElement.removeAttribute("data-theme");
  }, [theme]);
  const toggle = () => {
    const dark = theme ? theme === "dark" : window.matchMedia("(prefers-color-scheme: dark)").matches;
    const next = dark ? "light" : "dark";
    setTheme(next);
    try {
      localStorage.setItem("libra-theme", next);
    } catch {
      /* private mode */
    }
  };
  return [theme, toggle];
}

export default function Layout() {
  const { user, logout } = useAuth();
  const isAdmin = useCan("admin");
  const [open, setOpen] = useState(false);
  const [, toggleTheme] = useTheme();
  const nav = useNavigate();
  const loc = useLocation();
  useEffect(() => setOpen(false), [loc.pathname]);

  return (
    <div className="shell">
      <div className="topbar">
        <div className="brand" style={{ padding: 0 }}>
          <img src={asset("icon.svg")} alt="" />
          Libra
        </div>
        <button className="icon-btn" onClick={() => setOpen(!open)} aria-label="Menu">
          <Icon name="menu" size={22} />
        </button>
      </div>
      <aside className={`sidebar ${open ? "open" : ""}`}>
        <div className="brand">
          <img src={asset("icon.svg")} alt="" />
          Libra
        </div>
        <nav className="nav">
          <NavLink to="/experiments">
            <Icon name="flask" /> Experiments
          </NavLink>
          <NavLink to="/businesses">
            <Icon name="building" /> Businesses & metrics
          </NavLink>
          <NavLink to="/parameters">
            <Icon name="search" /> Parameters
          </NavLink>
          <NavLink to="/layers">
            <Icon name="layers" /> Traffic layers
          </NavLink>
          <NavLink to="/attributes">
            <Icon name="target" /> Targeting attributes
          </NavLink>
          <div className="nav-label">Operate</div>
          <NavLink to="/tools">
            <Icon name="wrench" /> Debug tools
          </NavLink>
          <NavLink to="/pipeline">
            <Icon name="pipe" /> Data pipeline
          </NavLink>
          <NavLink to="/integrate">
            <Icon name="chart" /> Integration guide
          </NavLink>
          {isAdmin && (
            <NavLink to="/settings">
              <Icon name="gear" /> Settings
            </NavLink>
          )}
        </nav>
        <div className="sidebar-foot">
          <div className="whoami">
            {user?.display_name}
            <small>{user?.role}</small>
          </div>
          <div className="row" style={{ padding: "0 4px" }}>
            <button className="btn btn-ghost btn-sm" onClick={toggleTheme} title="Toggle theme">
              <Icon name="moon" /> Theme
            </button>
            <button
              className="btn btn-ghost btn-sm"
              onClick={async () => {
                await logout();
                nav("/login");
              }}
            >
              <Icon name="logout" /> Sign out
            </button>
          </div>
        </div>
      </aside>
      <main className="main">
        <Outlet />
      </main>
    </div>
  );
}
