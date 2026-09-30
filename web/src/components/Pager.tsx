import { useEffect, useState } from "react";

export const PAGE_SIZES = [10, 25, 50, 100];

// usePaged slices a list into pages; the page resets when the list's
// identity (e.g. a new search) changes shape.
export function usePaged<T>(items: T[], initialSize = 25, resetKey: unknown = null) {
  const [page, setPage] = useState(1);
  const [size, setSize] = useState(initialSize);
  const pages = Math.max(1, Math.ceil(items.length / size));
  useEffect(() => setPage(1), [resetKey, size]);
  useEffect(() => {
    if (page > pages) setPage(pages);
  }, [page, pages]);
  const slice = items.slice((page - 1) * size, page * size);
  return { slice, page, setPage, size, setSize, pages, total: items.length };
}

// Pager: "21–40 of 312", page buttons and a page-size choice.
export function Pager({
  page,
  pages,
  total,
  size,
  onPage,
  onSize,
  noun = "items",
}: {
  page: number;
  pages: number;
  total: number;
  size: number;
  onPage: (p: number) => void;
  onSize: (s: number) => void;
  noun?: string;
}) {
  if (total === 0) return null;
  const from = (page - 1) * size + 1;
  const to = Math.min(total, page * size);
  // A window of page numbers around the current one.
  const nums: (number | "…")[] = [];
  for (let p = 1; p <= pages; p++) {
    if (p === 1 || p === pages || Math.abs(p - page) <= 1) nums.push(p);
    else if (nums[nums.length - 1] !== "…") nums.push("…");
  }
  return (
    <div className="pager">
      <span className="small faint">
        {from}–{to} of {total.toLocaleString()} {noun}
      </span>
      <span className="spacer" />
      {pages > 1 && (
        <div className="row" style={{ gap: 4 }}>
          <button className="btn btn-sm" disabled={page <= 1} onClick={() => onPage(page - 1)} aria-label="Previous page">
            ‹
          </button>
          {nums.map((n, i) =>
            n === "…" ? (
              <span key={`e${i}`} className="faint small" style={{ padding: "0 4px" }}>
                …
              </span>
            ) : (
              <button key={n} className={`btn btn-sm ${n === page ? "btn-primary" : ""}`} onClick={() => onPage(n)}>
                {n}
              </button>
            )
          )}
          <button className="btn btn-sm" disabled={page >= pages} onClick={() => onPage(page + 1)} aria-label="Next page">
            ›
          </button>
        </div>
      )}
      <select className="input" style={{ width: 110, height: 30 }} value={size} onChange={(e) => onSize(Number(e.target.value))} aria-label="Per page">
        {PAGE_SIZES.map((s) => (
          <option key={s} value={s}>
            {s} / page
          </option>
        ))}
      </select>
    </div>
  );
}
