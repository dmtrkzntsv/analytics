/* analytics tracking snippet — cookieless (spec §5.5).
 * <script defer src="https://a.example.com/js/script.js" data-project="myapp"></script>
 * Optional: data-user="uid" to attribute product events.
 */
(function () {
  "use strict";
  var script = document.currentScript;
  if (!script) return;
  var project = script.getAttribute("data-project");
  if (!project) return;
  var endpoint = new URL(script.src).origin;
  var userId = script.getAttribute("data-user") || null;
  var anonId = "anon-" + Math.random().toString(36).slice(2, 12);

  function ignored() {
    try { if (localStorage.analytics_ignore === "true") return true; } catch (e) {}
    if (/^localhost$|^127(\.\d+){3}$|^\[::1\]$/.test(location.hostname)) return true;
    if (location.protocol === "file:") return true;
    if (navigator.webdriver) return true;
    return false;
  }

  function send(path, payload) {
    var body = JSON.stringify(payload);
    // sendBeacon with a string posts text/plain: no CORS preflight.
    if (navigator.sendBeacon && navigator.sendBeacon(endpoint + path, body)) return;
    fetch(endpoint + path, { method: "POST", body: body, keepalive: true });
  }

  var lastPage = null;
  function page() {
    if (ignored()) return;
    var current = location.pathname + location.search;
    if (current === lastPage) return;
    lastPage = current;
    send("/api/hit", { project: project, url: location.href, referrer: document.referrer });
  }

  function track(name, attributes) {
    if (ignored() || !name) return;
    send("/api/event", {
      project: project,
      name: String(name),
      user_id: userId || anonId,
      attributes: attributes || {}
    });
  }

  var pushState = history.pushState;
  history.pushState = function () {
    pushState.apply(this, arguments);
    page();
  };
  window.addEventListener("popstate", page);

  window.analytics = {
    track: track,
    identify: function (id) { userId = id ? String(id) : null; }
  };

  page();
})();
