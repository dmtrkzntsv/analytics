/* Path-shaping helpers, exposed as twillingate.util for use inside a
 * page() listener or via data-mask-url. They exist so a site does not
 * hand-roll the same regexes: getting the uuid pattern subtly wrong, or
 * forgetting to sort query parameters, are both silent failures that
 * surface weeks later as a blown-out pages breakdown.
 */

const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;
const ULID = /^[0-7][0-9A-HJKMNP-TV-Z]{25}$/;
const NUMERIC = /^\d+$/;
const HEX = /^[0-9a-f]{24,}$/i;
const SCHEME = /^[a-z][a-z0-9+.-]*:\/\//i;

export interface MaskOptions {
  /** Also mask all-digit segments. Off by default: /2024/report is real. */
  numeric?: boolean;
  /** Also mask 24+ character hex blobs (Mongo ObjectIds and similar). */
  hex?: boolean;
}

function maskSegments(s: string, opts: MaskOptions): string {
  return s
    .split("/")
    .map((seg) => {
      if (UUID.test(seg) || ULID.test(seg)) return "[id]";
      if (opts.numeric && NUMERIC.test(seg)) return "[id]";
      if (opts.hex && HEX.test(seg)) return "[id]";
      return seg;
    })
    .join("/");
}

/**
 * Replace identifier-shaped path segments with [id].
 *
 * Given an absolute URL this masks the path and the hash but never the
 * host: a hostname is not a path segment, and in hash-routing mode the
 * route lives in the hash. The query is left untouched so campaign
 * parameters survive. Given a bare path, the whole input is masked.
 *
 * Only shapes that are never legitimate route names are masked by default
 * -- UUIDs and ULIDs. Numeric and hex are opt-in because /2024/report and
 * a hex slug are both real paths.
 */
export function maskIds(value: string, opts: MaskOptions = {}): string {
  const scheme = SCHEME.exec(value);
  if (!scheme) return maskSegments(value, opts);

  const rest = value.slice(scheme[0].length);
  const slash = rest.indexOf("/");
  if (slash < 0) return value; // scheme + authority only: no path to mask
  const prefix = value.slice(0, scheme[0].length + slash);
  let tail = rest.slice(slash);

  let hash = "";
  const h = tail.indexOf("#");
  if (h >= 0) {
    hash = tail.slice(h + 1);
    tail = tail.slice(0, h);
  }
  let query = "";
  const q = tail.indexOf("?");
  if (q >= 0) {
    query = tail.slice(q);
    tail = tail.slice(0, q);
  }

  return prefix + maskSegments(tail, opts) + query +
    (hash ? "#" + maskSegments(hash, opts) : "");
}

/**
 * Append allowlisted query parameters to a path, sorted by key.
 *
 * Sorting is load-bearing: without it ?a=1&b=2 and ?b=2&a=1 become two
 * rows in the pages breakdown for one page. Allowlist only low-cardinality
 * parameters -- never identifiers or free text.
 */
export function withQuery(path: string, url: string, keys: string[]): string {
  let params: URLSearchParams;
  try {
    params = new URL(url, "http://localhost").searchParams;
  } catch {
    return path;
  }
  const picked: string[] = [];
  for (const key of [...keys].sort()) {
    const v = params.get(key);
    if (v !== null) picked.push(`${encodeURIComponent(key)}=${encodeURIComponent(v)}`);
  }
  return picked.length ? `${path}?${picked.join("&")}` : path;
}
