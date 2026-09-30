import { useRef, type KeyboardEvent, type ReactNode } from "react";

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
}: {
  value: string;
  onChange: (v: string) => void;
  disabled?: boolean;
  rows?: number;
  invalid?: boolean;
}) {
  const ref = useRef<HTMLTextAreaElement>(null);
  const escaped = useRef(false);

  // Replace [from, to) with text and select [selA, selB) afterwards. Uses
  // execCommand where possible so the browser's undo stack keeps working.
  const edit = (from: number, to: number, text: string, selA: number, selB: number) => {
    const el = ref.current!;
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
  return (
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
}

// NamespacedJson frames an editor with the fixed platform wrapper: what's
// typed goes inside {"<platform key>": …}, so platforms never collide.
export function NamespacedJson({ platformKey, platformName, children }: { platformKey: string; platformName: string; children: ReactNode }) {
  return (
    <div className="json-ns">
      <div className="json-ns-line" title={`Parameters are namespaced by the platform key of ${platformName || "the business's platform"}`}>
        {"{ "}
        <span className="json-ns-key">"{platformKey || "…"}"</span>: <span className="faint">— {platformName || "platform"}, fixed</span>
      </div>
      <div className="json-ns-body">{children}</div>
      <div className="json-ns-line">{"}"}</div>
    </div>
  );
}
