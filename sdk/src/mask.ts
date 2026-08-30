import { maskIds, type MaskOptions } from "./util";

/**
 * What data-mask-url and init({maskUrl}) accept. The attribute can only
 * carry a string; init additionally takes the native values. A string
 * resolves through this one function either way, so the two entry points
 * cannot drift apart.
 */
export type MaskSpec = string | RegExp | ((href: string) => string);

/** Token names accepted for the built-in masker. */
const BUILTINS = new Set(["uuid", "numeric", "hex"]);

/** Parse a "/pattern/flags" literal. Returns null if it will not compile. */
function parseRegExp(spec: string): RegExp | null {
  const end = spec.lastIndexOf("/");
  if (end <= 0) return null;
  try {
    return new RegExp(spec.slice(1, end), spec.slice(end + 1));
  } catch {
    return null;
  }
}

/**
 * Resolve a mask spec to a function over the full href.
 *
 * Returns undefined when nothing was asked for, and null when something
 * was asked for and could not be provided. Null is the fail-closed signal:
 * the caller drops pageviews rather than sending unmasked ones, because a
 * site that configured masking and typo'd the name must not silently ship
 * /account/8812.
 */
export function resolveMask(
  spec: MaskSpec | undefined | null,
): ((href: string) => string) | null | undefined {
  if (spec === undefined || spec === null || spec === "") return undefined;

  if (typeof spec === "function") return spec;
  if (spec instanceof RegExp) return (href) => href.replace(spec, "[id]");

  if (spec.startsWith("/")) {
    const re = parseRegExp(spec);
    if (!re) {
      console.warn(`twillingate: mask is not a valid regexp, dropping pageviews: ${spec}`);
      return null;
    }
    return (href) => href.replace(re, "[id]");
  }

  const tokens = spec.split(",").map((t) => t.trim()).filter(Boolean);
  if (tokens.length > 0 && tokens.every((t) => BUILTINS.has(t))) {
    const opts: MaskOptions = {
      numeric: tokens.includes("numeric"),
      hex: tokens.includes("hex"),
    };
    return (href) => maskIds(href, opts);
  }

  const fn = (globalThis as Record<string, unknown>)[spec];
  if (typeof fn !== "function") {
    console.warn(`twillingate: mask names no function, dropping pageviews: ${spec}`);
    return null;
  }
  return fn as (href: string) => string;
}
