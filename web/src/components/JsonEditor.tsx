import { useRef, useState, type KeyboardEvent, type ReactNode } from "react";
import { ErrorBox, Modal } from "./ui";

const INDENT = "  ";

// JsonEditor is a textarea that edits like a code editor: Tab / Shift+Tab
// indent and outdent (the selected lines, or at the cursor), Enter keeps
// the indentation and opens a block after "{" or "[", and a closing bracket
// typed on an empty line snaps back one level. Esc then Tab moves focus on,
// as in other editors.
export function JsonEditor({
  value,
  onChange,
  disabled,
  rows,
  invalid,
  stringTools,
}: {
  value: string;
  onChange: (v: string) => void;
  disabled?: boolean;
  rows?: number;
  invalid?: boolean;
  stringTools?: boolean; // show the JSON-string helper
}) {
  const ref = useRef<HTMLTextAreaElement>(null);
  const escaped = useRef(false);

  // Replace [from, to) with text and select [selA, selB) afterwards. Uses
  // execCommand where possible so the browser's undo stack keeps working.
  const edit = (from: number, to: number, text: string, selA: number, selB: number) => {
    const el = ref.current!;
    // insertText goes to the focused element: make that the editor (it may
    // be called from a dialog).
    if (document.activeElement !== el) el.focus();
    el.setSelectionRange(from, to);
    const ok = typeof document.execCommand === "function" && document.execCommand("insertText", false, text);
    if (!ok) onChange(value.slice(0, from) + text + value.slice(to));
    requestAnimationFrame(() => el.setSelectionRange(selA, selB));
  };

  const onKeyDown = (e: KeyboardEvent<HTMLTextAreaElement>) => {
    const el = e.currentTarget;
    const { selectionStart: a, selectionEnd: b } = el;
    if (e.key === "Escape") {
      escaped.current = true;
      return;
    }
    if (e.key === "Tab") {
      if (escaped.current) {
        escaped.current = false;
        return; // let focus move on
      }
      e.preventDefault();
      const lineStart = value.lastIndexOf("\n", a - 1) + 1;
      const multi = value.slice(a, b).includes("\n");
      if (!multi && !e.shiftKey) {
        edit(a, b, INDENT, a + INDENT.length, a + INDENT.length);
        return;
      }
      // Indent / outdent every line touched by the selection.
      const endBase = b > a && value[b - 1] === "\n" ? b - 1 : b;
      const lineEnd = value.indexOf("\n", endBase);
      const blockEnd = lineEnd === -1 ? value.length : lineEnd;
      const lines = value.slice(lineStart, blockEnd).split("\n");
      let firstDelta = 0;
      const out = lines.map((l, i) => {
        if (e.shiftKey) {
          const n = l.startsWith(INDENT) ? INDENT.length : l.startsWith(" ") || l.startsWith("\t") ? 1 : 0;
          if (i === 0) firstDelta = -Math.min(n, a - lineStart);
          return l.slice(n);
        }
        if (i === 0) firstDelta = INDENT.length;
        return INDENT + l;
      });
      const text = out.join("\n");
      if (text === value.slice(lineStart, blockEnd)) return;
      edit(lineStart, blockEnd, text, multi ? lineStart : a + firstDelta, multi ? lineStart + text.length : b + firstDelta);
      return;
    }
    escaped.current = false;
    if (e.key === "Enter" && !e.metaKey && !e.ctrlKey && !e.altKey) {
      e.preventDefault();
      const lineStart = value.lastIndexOf("\n", a - 1) + 1;
      const indent = /^[ \t]*/.exec(value.slice(lineStart, a))![0];
      const before = value.slice(lineStart, a).trimEnd();
      const after = value.slice(b);
      const opens = before.endsWith("{") || before.endsWith("[");
      const closes = /^\s*[}\]]/.test(after) && (after.trimStart()[0] === "}" || after.trimStart()[0] === "]");
      if (opens && closes) {
        const text = `\n${indent}${INDENT}\n${indent}`;
        const p = a + 1 + indent.length + INDENT.length;
        edit(a, b + (after.length - after.trimStart().length), text, p, p);
      } else {
        const text = `\n${indent}${opens ? INDENT : ""}`;
        edit(a, b, text, a + text.length, a + text.length);
      }
      return;
    }
    if ((e.key === "}" || e.key === "]") && a === b) {
      const lineStart = value.lastIndexOf("\n", a - 1) + 1;
      const typed = value.slice(lineStart, a);
      if (/^[ \t]+$/.test(typed) && typed.length >= INDENT.length) {
        e.preventDefault();
        const text = typed.slice(INDENT.length) + e.key;
        edit(lineStart, a, text, lineStart + text.length, lineStart + text.length);
      }
    }
  };

  const lines = value.split("\n").length;
  const area = (
    <textarea
      ref={ref}
      className="input input-mono json-editor"
      style={invalid ? { borderColor: "var(--bad)" } : undefined}
      rows={rows ?? Math.min(18, Math.max(4, lines + 1))}
      disabled={disabled}
      value={value}
      spellCheck={false}
      autoCapitalize="off"
      autoCorrect="off"
      onChange={(e) => onChange(e.target.value)}
      onKeyDown={onKeyDown}
      title="Tab / Shift+Tab indent · Esc then Tab to leave the editor"
    />
  );
  if (!stringTools || disabled) return area;
  return (
    <div className="stack-sm">
      {area}
      <JsonStringTool value={value} textarea={ref} replace={(from, to, text) => edit(from, to, text, from, from + text.length)} />
    </div>
  );
}

// stringTokenAt finds the JSON string literal around a position: [start, end)
// including its quotes.
function stringTokenAt(src: string, pos: number): [number, number] | null {
  let i = 0;
  while (i < src.length) {
    if (src[i] === '"') {
      const start = i++;
      while (i < src.length && src[i] !== '"') i += src[i] === "\\" ? 2 : 1;
      const end = Math.min(i + 1, src.length);
      if (pos >= start && pos <= end) return [start, end];
      i = end;
    } else i++;
  }
  return null;
}

function decodeJsonString(literal: string): unknown {
  try {
    const str = JSON.parse(literal);
    if (typeof str !== "string") return undefined;
    const t = str.trim();
    if (!(t.startsWith("{") || t.startsWith("["))) return undefined;
    const v = JSON.parse(t);
    return v && typeof v === "object" ? v : undefined;
  } catch {
    return undefined;
  }
}

// JsonStringTool: services often take JSON *inside a string* ("var_json":
// "{\"order\": …}"), which is painful to escape by hand. Paste or type the
// real JSON here and it goes in as an escaped string; or put the cursor in
// an existing JSON string and edit it as JSON.
function JsonStringTool({ value, textarea, replace }: { value: string; textarea: React.RefObject<HTMLTextAreaElement>; replace: (from: number, to: number, text: string) => void }) {
  const [state, setState] = useState<null | { mode: "insert" | "edit"; from: number; to: number; text: string; key: string }>(null);
  const [compact, setCompact] = useState(true);
  const [hint, setHint] = useState("");
  let parsed: unknown;
  let error = "";
  if (state) {
    try {
      parsed = JSON.parse(state.text);
    } catch (e) {
      error = state.text.trim() ? `Not valid JSON yet: ${e instanceof Error ? e.message : String(e)}` : "";
    }
  }
  const literal = parsed !== undefined ? JSON.stringify(compact ? JSON.stringify(parsed) : JSON.stringify(parsed, null, 2)) : "";
  const out = state?.mode === "insert" && state.key.trim() ? `${JSON.stringify(state.key.trim())}: ${literal}` : literal;

  const open = (mode: "insert" | "edit") => {
    setHint("");
    const el = textarea.current;
    const a = el?.selectionStart ?? value.length;
    const b = el?.selectionEnd ?? a;
    if (mode === "edit") {
      const tok = stringTokenAt(value, a);
      const decoded = tok ? decodeJsonString(value.slice(tok[0], tok[1])) : undefined;
      if (!tok || decoded === undefined) {
        setHint("Put the cursor inside a string that holds JSON (like \"{\\\"a\\\": 1}\") first.");
        return;
      }
      const s = JSON.parse(value.slice(tok[0], tok[1])) as string;
      setCompact(!s.includes("\n"));
      setState({ mode, from: tok[0], to: tok[1], text: JSON.stringify(decoded, null, 2), key: "" });
      return;
    }
    // A selected JSON value becomes the starting text.
    const sel = value.slice(a, b);
    let start = "";
    try {
      if (sel.trim()) start = JSON.stringify(JSON.parse(sel), null, 2);
    } catch {
      /* not JSON: start empty */
    }
    setState({ mode, from: a, to: start ? b : a, text: start, key: "" });
  };

  return (
    <div className="row small" style={{ gap: 8, flexWrap: "wrap" }}>
      <span className="faint">JSON string:</span>
      <button type="button" className="btn btn-sm" onClick={() => open("insert")} title="Paste or type real JSON; it's inserted as an escaped string at the cursor (a selected JSON value is converted in place)">
        Insert as string…
      </button>
      <button type="button" className="btn btn-sm" onClick={() => open("edit")} title="Put the cursor inside an existing JSON string to edit it as JSON">
        Edit string at cursor…
      </button>
      {hint && <span className="faint">{hint}</span>}
      {state && (
        <Modal
          wide
          title={state.mode === "edit" ? "Edit JSON string" : "Insert JSON as a string"}
          onClose={() => setState(null)}
          footer={
            <>
              <button className="btn" onClick={() => setState(null)}>
                Cancel
              </button>
              <button
                className="btn btn-primary"
                disabled={parsed === undefined}
                onClick={() => {
                  replace(state.from, state.to, out);
                  setState(null);
                }}
              >
                {state.mode === "edit" ? "Update string" : "Insert"}
              </button>
            </>
          }
        >
          <p className="small muted" style={{ marginTop: 0 }}>
            Paste or type the real JSON. It's escaped into a string value — what services that read a JSON string field expect.
          </p>
          {state.mode === "insert" && (
            <label className="stack-sm small">
              <b>Key (optional)</b>
              <input
                className="input input-mono"
                placeholder="var_json — leave empty to insert just the string value"
                value={state.key}
                onChange={(e) => setState({ ...state, key: e.target.value })}
              />
            </label>
          )}
          <textarea
            className="input input-mono"
            rows={12}
            autoFocus
            spellCheck={false}
            value={state.text}
            placeholder={'{\n  "order": "best_match",\n  "formula": "ctr * cvr * gmv"\n}'}
            onChange={(e) => setState({ ...state, text: e.target.value })}
            style={error ? { borderColor: "var(--bad)" } : undefined}
          />
          <label className="check small">
            <input type="checkbox" checked={compact} onChange={(e) => setCompact(e.target.checked)} /> Compact (one line, no spaces)
          </label>
          <ErrorBox error={error} />
          {out && (
            <div className="stack-sm">
              <b className="small">{state.mode === "edit" ? "Replaces the string with" : "Inserts"}</b>
              <pre className="code small json-raw">{out}</pre>
            </div>
          )}
        </Modal>
      )}
    </div>
  );
}

// NamespacedJson frames an editor with the fixed platform wrapper: what's
// typed goes inside {"<platform key>": …}, so platforms never collide.
export function NamespacedJson({ platformKey, platformName, children }: { platformKey: string; platformName: string; children: ReactNode }) {
  return (
    <div className="json-ns">
      <div className="json-ns-line" title={`Parameters are namespaced by the platform key of ${platformName || "the business's platform"}`}>
        {"{ "}
        {platformKey ? (
          <>
            <span className="json-ns-key">"{platformKey}"</span>: <span className="faint">— {platformName || "platform"} key, fixed</span>
          </>
        ) : (
          <span className="faint">"…": — choose a platform above; its key wraps these parameters</span>
        )}
      </div>
      <div className="json-ns-body">{children}</div>
      <div className="json-ns-line">{"}"}</div>
    </div>
  );
}
