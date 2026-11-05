(function () {
  var languageNames = {
    bash: "Bash",
    sh: "Shell",
    shell: "Shell",
    console: "Shell",
    json: "JSON",
    ts: "TypeScript",
    tsx: "TypeScript",
    js: "JavaScript",
    go: "Go",
    java: "Java",
    python: "Python",
    http: "HTTP",
    sql: "SQL",
    yaml: "YAML",
    yml: "YAML",
    toml: "TOML",
    ini: "INI",
    diff: "Diff",
    text: "Text"
  };

  document.querySelectorAll(".book-article pre").forEach(function (pre) {
    if (pre.closest(".code-frame")) return;
    var code = pre.querySelector("code");
    if (!code) return;

    var language = code.dataset.lang || "";
    if (!language) {
      var match = Array.from(code.classList).find(function (name) {
        return name.indexOf("language-") === 0;
      });
      if (match) language = match.slice("language-".length);
    }

    var frame = document.createElement("div");
    frame.className = "code-frame";

    var head = document.createElement("div");
    head.className = "code-frame__head";

    var label = document.createElement("span");
    label.className = "code-frame__lang";
    label.textContent = languageNames[language] || language || "Code";

    var button = document.createElement("button");
    button.type = "button";
    button.className = "code-frame__copy";
    button.textContent = "Copy";
    button.setAttribute("aria-label", "Copy code");

    head.append(label, button);

    // Hugo wraps highlighted blocks in <div class="highlight">. Wrap that
    // container rather than the inner <pre>, otherwise the frame's margin lands
    // inside the highlight border and leaves a blank strip above the header.
    var container = pre.parentElement;
    if (!container || !container.classList.contains("highlight")) {
      container = pre;
    }
    container.parentNode.insertBefore(frame, container);
    frame.append(head, container);

    button.addEventListener("click", function () {
      var text = code.innerText;
      var done = function () {
        button.textContent = "Copied";
        window.setTimeout(function () {
          button.textContent = "Copy";
        }, 1600);
      };
      if (navigator.clipboard && navigator.clipboard.writeText) {
        navigator.clipboard.writeText(text).then(done, function () {
          button.textContent = "Copy failed";
        });
      } else {
        var area = document.createElement("textarea");
        area.value = text;
        area.setAttribute("readonly", "");
        area.style.position = "fixed";
        area.style.opacity = "0";
        document.body.appendChild(area);
        area.select();
        try {
          document.execCommand("copy");
          done();
        } catch (error) {
          button.textContent = "Copy failed";
        }
        document.body.removeChild(area);
      }
    });
  });

  document.querySelectorAll(".docs-toc-mobile a").forEach(function (link) {
    link.addEventListener("click", function () {
      var details = link.closest("details");
      if (details) details.open = false;
    });
  });
})();
