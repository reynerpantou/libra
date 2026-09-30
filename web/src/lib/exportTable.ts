// Helpers to copy tables to the clipboard (tab-separated, pastes into
// sheets) and to download CSV files.

export type Cell = string | number | null | undefined;

const tsvCell = (c: Cell) => (c === null || c === undefined ? "" : String(c).replace(/[\t\n]+/g, " "));
const csvCell = (c: Cell) => {
  const s = c === null || c === undefined ? "" : String(c);
  return /[",\n]/.test(s) ? `"${s.replace(/"/g, '""')}"` : s;
};

export const toTSV = (rows: Cell[][]) => rows.map((r) => r.map(tsvCell).join("\t")).join("\n");
export const toCSV = (rows: Cell[][]) => rows.map((r) => r.map(csvCell).join(",")).join("\n");

export async function copyText(text: string): Promise<void> {
  try {
    await navigator.clipboard.writeText(text);
  } catch {
    // Older browsers / insecure origins: a hidden textarea still works.
    const ta = document.createElement("textarea");
    ta.value = text;
    ta.style.position = "fixed";
    ta.style.opacity = "0";
    document.body.appendChild(ta);
    ta.select();
    document.execCommand("copy");
    ta.remove();
  }
}

export function downloadCSV(name: string, rows: Cell[][]) {
  const blob = new Blob(["\ufeff" + toCSV(rows)], { type: "text/csv;charset=utf-8" });
  const url = URL.createObjectURL(blob);
  const a = document.createElement("a");
  a.href = url;
  a.download = name;
  document.body.appendChild(a);
  a.click();
  a.remove();
  setTimeout(() => URL.revokeObjectURL(url), 1000);
}
