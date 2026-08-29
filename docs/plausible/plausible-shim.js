/* plausible-shim.js — keeps Plausible's class-based tagging working after the
 * tracker swap. Serve it from the site (Plausible's own script is gone) and
 * load it AFTER the tracking snippet, which is what defines analytics.track:
 *
 *   <script defer src="https://a.example.com/js/script.js" data-key="ak_…"></script>
 *   <script defer src="/scripts/plausible-shim.js"></script>
 *
 *   <a class="plausible-event-name--signup_cloud" href="/register">
 *   → analytics.track("signup_cloud")
 *
 * See README.md in this folder for the class syntax and where the events land.
 */
(function () {
  "use strict";
  var PREFIX = "plausible-event-";

  // Plausible accepts two separators: `=` (primary) and `--` (for site
  // builders that strip `=` from class names). Split on whichever comes
  // first, so single hyphens inside a key or value stay intact.
  function split(rest) {
    var eq = rest.indexOf("="), dd = rest.indexOf("--");
    if (eq === -1 && dd === -1) return null;
    var at = eq === -1 ? dd : dd === -1 ? eq : Math.min(eq, dd);
    return [rest.slice(0, at), rest.slice(at + (at === dd ? 2 : 1))];
  }

  // `plausible-event-<key><sep><value>`; `+` stands in for a space, because
  // class names cannot contain one. `name` is the event, everything else a
  // custom attribute.
  function parse(el) {
    var name = null, props = {};
    var classes = ((el.getAttribute && el.getAttribute("class")) || "").split(/\s+/);
    for (var i = 0; i < classes.length; i++) {
      var c = classes[i];
      if (c.lastIndexOf(PREFIX, 0) !== 0) continue;
      var kv = split(c.slice(PREFIX.length));
      if (!kv) continue;
      var key = kv[0].replace(/\+/g, " "), value = kv[1].replace(/\+/g, " ");
      // A `$` key would be dropped server-side as an unrecognized reserved
      // attribute, so refuse it here rather than lose it silently.
      if (!key || key.charAt(0) === "$") continue;
      if (key.toLowerCase() === "name") name = value;
      else props[key] = value;
    }
    return name ? { el: el, name: name, props: props } : null;
  }

  // The tag may sit on an ancestor of what was clicked: an icon inside a
  // button, a span inside a link.
  function tagged(node) {
    for (var el = node; el && el.nodeType === 1; el = el.parentElement) {
      var t = parse(el);
      if (t) return t;
    }
    return null;
  }

  function handle(e) {
    if (e.type !== "submit" && e.button > 1) return; // main and middle click only
    var t = tagged(e.target);
    if (!t) return;
    // A tagged <form> fires on submit, anything else on click. Without this a
    // tagged form wrapping its own submit button counts twice.
    if ((t.el.tagName === "FORM") !== (e.type === "submit")) return;
    // Product events carry no page context of their own, so a nav CTA fires
    // identically from every page unless the path rides along as an attribute.
    if (!("path" in t.props)) t.props.path = location.pathname;
    if (window.analytics && window.analytics.track) window.analytics.track(t.name, t.props);
  }

  // Capture phase: a handler that stops propagation must not eat the event.
  document.addEventListener("click", handle, true);
  document.addEventListener("auxclick", handle, true); // middle-click opens links too
  document.addEventListener("submit", handle, true);

  // Manual `plausible(...)` calls, for markup that mixes tagging with the
  // scripted form.
  window.plausible = function (name, opts) {
    if (window.analytics && window.analytics.track) window.analytics.track(name, opts && opts.props);
    if (opts && typeof opts.callback === "function") opts.callback();
  };
})();
