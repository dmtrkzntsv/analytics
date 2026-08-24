/* analytics tracking snippet — cookieless by default.
 * <script defer src="https://a.example.com/js/script.js"
 *         data-key="ak_9f3c…" data-identity="anonymous"></script>
 * Optional: data-user="u_123" data-group="org_9" when the page is rendered
 * already knowing who is looking at it.
 */
(function () {
  "use strict";
  var script = document.currentScript;
  if (!script) return;
  var key = script.getAttribute("data-key");
  if (!key) return;

  var endpoint = new URL(script.src).origin;
  // data-identity only authorizes writing to localStorage. The server is the
  // enforcement point: an anonymous project salts whatever arrives no matter
  // what this tag claims, so a misconfigured snippet fails safe.
  var identified = script.getAttribute("data-identity") === "identified";
  var userId = script.getAttribute("data-user") || null;
  var groupId = script.getAttribute("data-group") || null;

  var VISITOR = "analytics_visitor";
  var USER = "analytics_user";
  var GROUP = "analytics_group";

  function ls(name) {
    try { return localStorage.getItem(name); } catch (e) { return null; }
  }
  function lsSet(name, value) {
    try {
      if (value === null) localStorage.removeItem(name);
      else localStorage.setItem(name, value);
    } catch (e) {}
  }

  function ignored() {
    if (ls("analytics_ignore") === "true") return true;
    if (/^localhost$|^127(\.\d+){3}$|^\[::1\]$/.test(location.hostname)) return true;
    if (location.protocol === "file:") return true;
    if (navigator.webdriver) return true;
    return false;
  }

  function uuid() {
    if (window.crypto && crypto.randomUUID) return crypto.randomUUID();
    return "xxxxxxxx-xxxx-4xxx-yxxx-xxxxxxxxxxxx".replace(/[xy]/g, function (c) {
      var r = (Math.random() * 16) | 0;
      return (c === "x" ? r : (r & 0x3) | 0x8).toString(16);
    });
  }

  // Identity precedence: a site-supplied id, else a stored visitor id, else
  // nothing — in which case the server falls back to its rotating hash.
  // A persistent visitor id is terminal-equipment storage under ePrivacy, so
  // it is written only when the project opted into identified mode.
  function visitorId() {
    if (!identified) return null;
    var v = ls(VISITOR);
    if (!v) { v = uuid(); lsSet(VISITOR, v); }
    return v;
  }

  function attributes() {
    var a = {};
    var u = userId || (identified ? ls(USER) : null);
    var g = groupId || ls(GROUP);
    if (u) a.$user_id = u;
    if (g) a.$group_id = g;
    var v = visitorId();
    if (v) a.$install_id = v;
    return a;
  }

  function send(events) {
    var body = JSON.stringify({ key: key, attributes: attributes(), events: events });
    // sendBeacon with a string posts text/plain: a CORS-simple request with
    // no preflight, and it survives page unload. It cannot set headers,
    // which is why the key travels in the body.
    if (navigator.sendBeacon && navigator.sendBeacon(endpoint + "/api/events", body)) return;
    fetch(endpoint + "/api/events", { method: "POST", body: body, keepalive: true });
  }

  function emit(name, attrs) {
    send([{ id: uuid(), ts: new Date().toISOString(), name: name, attributes: attrs || {} }]);
  }

  var lastPage = null;
  function page() {
    if (ignored()) return;
    var current = location.pathname + location.search;
    if (current === lastPage) return;
    lastPage = current;
    emit("$pageview", { $url: location.href, $referrer: document.referrer });
  }

  function track(name, attrs) {
    if (ignored() || !name) return;
    emit(String(name), attrs);
  }

  var pushState = history.pushState;
  history.pushState = function () {
    pushState.apply(this, arguments);
    page();
  };
  window.addEventListener("popstate", page);

  window.analytics = {
    track: track,
    // Persisted, so every later event — this page and future page loads —
    // carries the identity. Events already sent stay unattributed: there is
    // no retroactive stitching.
    identify: function (id, group) {
      userId = id ? String(id) : null;
      if (group) groupId = String(group);
      if (identified) lsSet(USER, userId);
      lsSet(GROUP, groupId);
    },
    // Required on logout. Without it the next person to use a shared browser
    // inherits the previous user's identity.
    reset: function () {
      userId = null;
      groupId = null;
      lsSet(USER, null);
      lsSet(GROUP, null);
      lsSet(VISITOR, null);
    }
  };

  page();
})();
