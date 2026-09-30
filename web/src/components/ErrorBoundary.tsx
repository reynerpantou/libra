import { Component, type ReactNode } from "react";

// ErrorBoundary keeps a render error in one page from blanking the whole
// app: it shows what went wrong and a way back.
export class ErrorBoundary extends Component<{ children: ReactNode }, { error: Error | null }> {
  state = { error: null as Error | null };
  static getDerivedStateFromError(error: Error) {
    return { error };
  }
  componentDidCatch(error: Error) {
    console.error(error);
  }
  render() {
    if (!this.state.error) return this.props.children;
    return (
      <div className="page">
        <div className="alert alert-bad stack-sm">
          <b>This page hit an error and couldn't be shown.</b>
          <span className="mono small">{this.state.error.message}</span>
          <span className="small">Reload the page; if it keeps happening, report the message above.</span>
          <div>
            <button className="btn btn-sm" onClick={() => window.location.reload()}>
              Reload
            </button>
          </div>
        </div>
      </div>
    );
  }
}
