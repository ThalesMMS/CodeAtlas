(() => {
  "use strict";

  const dataElement = document.getElementById("codemap-data");
  const sourceElement = document.getElementById("codemap-sources");
  if (!dataElement || !sourceElement) {
    throw new Error("Missing embedded codemap payload");
  }

  const data = JSON.parse(dataElement.textContent || "{}");
  const sourceSnapshot = JSON.parse(sourceElement.textContent || "{}");
  const nodeById = new Map((data.nodes || []).map((node) => [node.id, node]));
  const sectionById = new Map((data.sections || []).map((section) => [section.id, section]));
  const groupById = new Map((data.groups || []).map((group) => [group.id, group]));
  const sectionDetails = new Map();
  const nodeControls = new Map();
  const mapLayout = new Map();
  const traceOrder = new Map();

  const STRINGS = {
    en: {
      guide: "Guide",
      map: "Map",
      view: "View",
      created: "Created",
      repository: "Repository",
      revision: "Revision",
      workingTree: "Working tree",
      uncertainty: "Material uncertainty",
      seeMore: "See more",
      seeLess: "See less",
      generatedGuide: "AI generated guide",
      motivation: "Motivation",
      details: "Details",
      source: "Source",
      selectNode: "Select a node",
      sourceEmpty: "Select a reference, guide card, or map node to inspect the exact source range.",
      relations: "Relations",
      verified: "Verified",
      inferred: "Inferred",
      copyLink: "Copy deep link",
      copyPath: "Copy source path",
      closeSource: "Close source",
      copiedLink: "Deep link copied",
      copiedPath: "Source path copied",
      copyFailed: "Could not copy automatically",
      theme: "Theme",
      themeSystem: "System theme",
      themeLight: "Light theme",
      themeDark: "Dark theme",
      zoomIn: "Zoom in",
      zoomOut: "Zoom out",
      resetMap: "Reset map",
      interactiveMap: "Interactive codemap canvas",
      outgoing: "outgoing",
      incoming: "incoming",
      evidence: "Evidence",
      reason: "Why inferred",
      clean: "clean",
      dirty: "dirty",
      unknown: "unknown"
    },
    "pt-BR": {
      guide: "Guia",
      map: "Mapa",
      view: "Visualização",
      created: "Criado em",
      repository: "Repositório",
      revision: "Revisão",
      workingTree: "Árvore de trabalho",
      uncertainty: "Incerteza material",
      seeMore: "Ver mais",
      seeLess: "Ver menos",
      generatedGuide: "Guia gerado por IA",
      motivation: "Motivação",
      details: "Detalhes",
      source: "Código-fonte",
      selectNode: "Selecione um nó",
      sourceEmpty: "Selecione uma referência, um card do guia ou um nó do mapa para inspecionar o intervalo exato de código.",
      relations: "Relações",
      verified: "Verificado",
      inferred: "Inferido",
      copyLink: "Copiar link direto",
      copyPath: "Copiar caminho do código",
      closeSource: "Fechar código-fonte",
      copiedLink: "Link direto copiado",
      copiedPath: "Caminho do código copiado",
      copyFailed: "Não foi possível copiar automaticamente",
      theme: "Tema",
      themeSystem: "Tema do sistema",
      themeLight: "Tema claro",
      themeDark: "Tema escuro",
      zoomIn: "Aumentar zoom",
      zoomOut: "Reduzir zoom",
      resetMap: "Redefinir mapa",
      interactiveMap: "Canvas interativo do codemap",
      outgoing: "saída",
      incoming: "entrada",
      evidence: "Evidência",
      reason: "Motivo da inferência",
      clean: "limpa",
      dirty: "alterada",
      unknown: "desconhecida"
    }
  };

  const locale = String(data.meta?.language || "en").toLowerCase().startsWith("pt") ? "pt-BR" : "en";
  const dictionary = STRINGS[locale] || STRINGS.en;
  const t = (key) => dictionary[key] || STRINGS.en[key] || key;

  const els = {
    shell: document.getElementById("codemap-shell"),
    title: document.getElementById("codemap-title"),
    metadata: document.getElementById("codemap-metadata"),
    summary: document.getElementById("codemap-summary"),
    uncertainty: document.getElementById("codemap-uncertainty"),
    sections: document.getElementById("codemap-sections"),
    guideView: document.getElementById("guide-view"),
    mapView: document.getElementById("map-view"),
    guideButton: document.getElementById("view-guide"),
    mapButton: document.getElementById("view-map"),
    guideLabel: document.getElementById("view-guide-label"),
    mapLabel: document.getElementById("view-map-label"),
    themeButton: document.getElementById("theme-toggle"),
    mapStage: document.getElementById("map-stage"),
    mapWorld: document.getElementById("map-world"),
    mapEdges: document.getElementById("map-edges"),
    mapGroups: document.getElementById("map-groups"),
    mapNodes: document.getElementById("map-nodes"),
    zoomIn: document.getElementById("map-zoom-in"),
    zoomOut: document.getElementById("map-zoom-out"),
    resetMap: document.getElementById("map-reset"),
    sourcePanel: document.getElementById("source-panel"),
    sourceKicker: document.getElementById("source-panel-kicker"),
    sourceTitle: document.getElementById("source-panel-title"),
    sourceLocation: document.getElementById("source-location"),
    sourceEmpty: document.getElementById("source-empty"),
    sourceEmptyText: document.getElementById("source-empty-text"),
    sourceContent: document.getElementById("source-content"),
    sourceStatus: document.getElementById("source-status-row"),
    sourceCode: document.getElementById("source-code"),
    relationsTitle: document.getElementById("relations-title"),
    sourceRelations: document.getElementById("source-relations"),
    copyLink: document.getElementById("copy-link"),
    copyPath: document.getElementById("copy-path"),
    sourceClose: document.getElementById("source-close"),
    sourceBackdrop: document.getElementById("source-backdrop"),
    toast: document.getElementById("toast")
  };

  const state = {
    view: data.meta?.defaultView === "map" ? "map" : "guide",
    selectedNodeId: null,
    theme: loadTheme(),
    lastFocused: null,
    keyboardInput: false,
    toastTimer: null,
    map: { x: 0, y: 0, scale: 1, worldWidth: 1200, worldHeight: 800 },
    pan: null,
    mapInitialized: false
  };

  const overlayMedia = window.matchMedia("(max-width: 1120px)");
  const svgNS = "http://www.w3.org/2000/svg";

  function loadTheme() {
    const configured = data.presentation?.theme || "system";
    try {
      const stored = window.localStorage.getItem("codemap-theme");
      if (["system", "light", "dark"].includes(stored)) return stored;
    } catch (_) {
      // Local storage is optional under file:// and privacy modes.
    }
    return ["system", "light", "dark"].includes(configured) ? configured : "system";
  }

  function saveTheme(theme) {
    try {
      window.localStorage.setItem("codemap-theme", theme);
    } catch (_) {
      // Theme persistence is optional.
    }
  }

  function createElement(tag, options = {}) {
    const element = document.createElement(tag);
    if (options.className) element.className = options.className;
    if (options.text !== undefined) element.textContent = String(options.text);
    if (options.id) element.id = options.id;
    if (options.type) element.type = options.type;
    if (options.title) element.title = options.title;
    if (options.hidden !== undefined) element.hidden = Boolean(options.hidden);
    if (options.attrs) {
      for (const [name, value] of Object.entries(options.attrs)) {
        if (value !== undefined && value !== null) element.setAttribute(name, String(value));
      }
    }
    if (options.dataset) {
      for (const [name, value] of Object.entries(options.dataset)) {
        element.dataset[name] = String(value);
      }
    }
    return element;
  }

  function createSvgElement(tag, attrs = {}) {
    const element = document.createElementNS(svgNS, tag);
    for (const [name, value] of Object.entries(attrs)) {
      element.setAttribute(name, String(value));
    }
    return element;
  }

  const ICONS = {
    guide: ["M4 5h16", "M4 9h11", "M4 13h16", "M4 17h9", "M18 8v8", "m15 13 3 3 3-3"],
    map: ["M4 5l5-2 6 2 5-2v16l-5 2-6-2-5 2V5Z", "M9 3v16", "M15 5v16"],
    theme: ["M12 3a9 9 0 1 0 9 9c0-.5-.04-1-.12-1.47A7 7 0 0 1 13.47 3.1C13 3.04 12.5 3 12 3Z"],
    link: ["M10 13a5 5 0 0 0 7.54.54l2-2a5 5 0 0 0-7.07-7.07l-1.15 1.15", "M14 11a5 5 0 0 0-7.54-.54l-2 2a5 5 0 0 0 7.07 7.07l1.15-1.15"],
    copy: ["M8 8h11v11H8z", "M16 8V5H5v11h3"],
    close: ["m6 6 12 12", "M18 6 6 18"],
    code: ["m8 9-3 3 3 3", "m16 9 3 3-3 3", "m14 5-4 14"],
    chevron: ["m9 6 6 6-6 6"]
  };

  function makeIcon(name) {
    const svg = createSvgElement("svg", {
      viewBox: "0 0 24 24",
      fill: "none",
      stroke: "currentColor",
      "stroke-width": "1.7",
      "stroke-linecap": "round",
      "stroke-linejoin": "round",
      focusable: "false",
      "aria-hidden": "true"
    });
    for (const d of ICONS[name] || []) {
      svg.appendChild(createSvgElement("path", { d }));
    }
    return svg;
  }

  function initializeIcons() {
    for (const holder of document.querySelectorAll("[data-icon]")) {
      holder.replaceChildren(makeIcon(holder.dataset.icon));
    }
  }

  function appendReferencedText(container, text, options = {}) {
    const source = String(text || "");
    const pattern = /\[([1-9][0-9]*[a-z]+(?:\s*,\s*[1-9][0-9]*[a-z]+)*)\]/g;
    let cursor = 0;
    let match;
    while ((match = pattern.exec(source)) !== null) {
      if (match.index > cursor) container.appendChild(document.createTextNode(source.slice(cursor, match.index)));
      const ids = match[1].split(",").map((id) => id.trim());
      container.appendChild(document.createTextNode("["));
      ids.forEach((id, index) => {
        if (index > 0) container.appendChild(document.createTextNode(", "));
        if (nodeById.has(id) && options.interactive !== false) {
          const button = createElement("button", {
            className: "reference-link",
            text: id,
            type: "button",
            attrs: { "aria-label": `${t("source")}: ${id}` },
            dataset: { refNode: id }
          });
          button.addEventListener("click", () => selectNode(id, { focusPanel: state.keyboardInput, scrollGuide: true }));
          container.appendChild(button);
        } else {
          container.appendChild(document.createTextNode(id));
        }
      });
      container.appendChild(document.createTextNode("]"));
      cursor = pattern.lastIndex;
    }
    if (cursor < source.length) container.appendChild(document.createTextNode(source.slice(cursor)));
  }

  function appendHighlightedCode(container, source) {
    const text = String(source ?? "");
    const pattern = /(?<comment>\/\/.*$|(?:^|\s)#.*$)|(?<string>"(?:\\.|[^"\\])*"|'(?:\\.|[^'\\])*'|`(?:\\.|[^`\\])*`)|(?<keyword>\b(?:const|let|var|function|export|async|await|return|new|class|static|if|else|for|of|in|while|switch|case|break|continue|try|catch|finally|throw|import|from|extends|implements|public|private|protected|interface|type|struct|enum|protocol|func|guard|defer|package|private|readonly)\b)|(?<number>\b\d+(?:\.\d+)?\b)/gm;
    let cursor = 0;
    for (const match of text.matchAll(pattern)) {
      if (Number(match.index) > cursor) container.appendChild(document.createTextNode(text.slice(cursor, Number(match.index))));
      const token = createElement("span", { text: match[0] });
      const kind = Object.entries(match.groups || {}).find(([, value]) => value !== undefined)?.[0];
      if (kind) token.className = `tok-${kind}`;
      container.appendChild(token);
      cursor = Number(match.index) + match[0].length;
    }
    if (cursor < text.length) container.appendChild(document.createTextNode(text.slice(cursor)));
  }

  function registerNodeControl(id, control) {
    if (!nodeControls.has(id)) nodeControls.set(id, new Set());
    nodeControls.get(id).add(control);
  }

  function renderMetadata() {
    document.documentElement.lang = locale;
    document.title = data.meta.title;
    els.title.textContent = data.meta.title;
    els.metadata.replaceChildren();

    const created = new Date(data.meta.createdAt);
    const formattedDate = Number.isNaN(created.getTime())
      ? data.meta.createdAt
      : new Intl.DateTimeFormat(locale, { dateStyle: "medium", timeStyle: "short" }).format(created);

    const repository = data.meta.repository || {};
    const items = [
      [t("created"), formattedDate],
      [t("repository"), repository.name || ""],
      [t("revision"), repository.revision || ""],
      [t("workingTree"), t(repository.workingTree || "unknown")]
    ];

    items.forEach(([label, value], index) => {
      if (index > 0) els.metadata.appendChild(createElement("span", { className: "meta-separator", text: "•", attrs: { "aria-hidden": "true" } }));
      const item = createElement("span", { className: "meta-item" });
      item.appendChild(createElement("span", { className: "meta-label", text: label }));
      const valueElement = createElement("span", { className: "meta-value", text: value, title: value });
      item.appendChild(valueElement);
      els.metadata.appendChild(item);
    });

    els.summary.replaceChildren();
    appendReferencedText(els.summary, data.meta.summary);

    if (data.meta.uncertainty) {
      els.uncertainty.hidden = false;
      els.uncertainty.replaceChildren();
      const strong = createElement("strong", { text: `${t("uncertainty")}: ` });
      els.uncertainty.appendChild(strong);
      appendReferencedText(els.uncertainty, data.meta.uncertainty);
    }
  }

  function renderGuideBlock(guide) {
    const panel = createElement("div", { className: "guide-panel" });
    panel.appendChild(createElement("div", { className: "guide-kicker", text: t("generatedGuide") }));
    panel.appendChild(createElement("h3", { text: t("motivation") }));
    const motivation = createElement("p");
    appendReferencedText(motivation, guide.motivation);
    panel.appendChild(motivation);
    panel.appendChild(createElement("h3", { text: t("details") }));

    for (const block of guide.details || []) {
      if (block.type === "list") {
        const list = createElement("ul");
        for (const item of block.items || []) {
          const li = createElement("li");
          appendReferencedText(li, item);
          list.appendChild(li);
        }
        panel.appendChild(list);
      } else {
        const paragraph = createElement("p");
        appendReferencedText(paragraph, block.text || "");
        panel.appendChild(paragraph);
      }
    }
    return panel;
  }

  function renderTraceItem(item) {
    const li = createElement("li", { className: "tree-item" });
    if (item.type === "node") {
      const node = nodeById.get(item.nodeId);
      if (!node) return li;
      const button = createElement("button", {
        className: `node-card${node.status === "inferred" ? " is-inferred" : ""}`,
        type: "button",
        id: `guide-node-${node.id}`,
        dataset: { nodeId: node.id },
        attrs: { "aria-label": `${node.id}: ${node.title}, ${node.source.path}:${node.source.targetLine}` }
      });
      button.appendChild(createElement("span", { className: "node-id", text: node.id }));
      button.appendChild(createElement("span", { className: "node-title", text: node.title, title: node.title }));
      button.appendChild(createElement("span", {
        className: "node-location",
        text: `${node.source.path}:${node.source.targetLine}`,
        title: `${node.source.path}:${node.source.targetLine}`
      }));
      const snippet = createElement("span", { className: "node-snippet", title: node.source.snippet });
      appendHighlightedCode(snippet, node.source.snippet);
      button.appendChild(snippet);
      button.addEventListener("click", () => selectNode(node.id, { focusPanel: state.keyboardInput, scrollGuide: false }));
      registerNodeControl(node.id, button);
      li.appendChild(button);
      return li;
    }

    const label = createElement("div", {
      className: item.type === "note" ? "tree-note-label" : "tree-group-label"
    });
    appendReferencedText(label, item.label || "");
    li.appendChild(label);
    const children = createElement("ul", { className: "tree-children" });
    for (const child of item.children || []) children.appendChild(renderTraceItem(child));
    li.appendChild(children);
    return li;
  }

  function renderSections() {
    els.sections.replaceChildren();
    sectionDetails.clear();
    nodeControls.clear();
    traceOrder.clear();
    let traceIndex = 0;

    function recordTrace(items) {
      for (const item of items || []) {
        if (item.type === "node" && !traceOrder.has(item.nodeId)) traceOrder.set(item.nodeId, traceIndex++);
        if (item.children) recordTrace(item.children);
      }
    }
    for (const section of data.sections || []) recordTrace(section.trace?.items || []);

    for (const section of data.sections || []) {
      const details = createElement("details", {
        className: "codemap-section",
        attrs: { "data-section-id": section.id }
      });
      details.open = Boolean(section.defaultOpen);
      sectionDetails.set(section.id, details);

      const summary = createElement("summary");
      const row = createElement("div", { className: "section-summary-row" });
      row.appendChild(createElement("div", { className: "section-number", text: section.number }));
      const heading = createElement("div", { className: "section-heading" });
      heading.appendChild(createElement("div", { className: "section-title", text: section.title }));
      heading.appendChild(createElement("div", { className: "section-description", text: section.summary }));
      row.appendChild(heading);
      const chevron = createElement("div", { className: "section-chevron", attrs: { "aria-hidden": "true" } });
      chevron.appendChild(makeIcon("chevron"));
      row.appendChild(chevron);
      summary.appendChild(row);
      details.appendChild(summary);

      const body = createElement("div", { className: "section-body" });
      if (section.guide) {
        const guideDetails = createElement("details", { className: "guide-details" });
        const guideSummary = createElement("summary", { text: t("seeMore") });
        guideDetails.addEventListener("toggle", () => {
          guideSummary.textContent = guideDetails.open ? t("seeLess") : t("seeMore");
        });
        guideDetails.appendChild(guideSummary);
        guideDetails.appendChild(renderGuideBlock(section.guide));
        body.appendChild(guideDetails);
      }

      const trace = createElement("div", { className: "trace" });
      trace.appendChild(createElement("h2", { className: "trace-title", text: section.trace?.title || section.title }));
      const tree = createElement("ul", { className: "trace-tree" });
      for (const item of section.trace?.items || []) tree.appendChild(renderTraceItem(item));
      trace.appendChild(tree);
      body.appendChild(trace);
      details.appendChild(body);
      els.sections.appendChild(details);
    }
  }

  function renderSource(node) {
    if (!node) {
      els.sourceKicker.textContent = t("source");
      els.sourceTitle.textContent = t("selectNode");
      els.sourceLocation.textContent = "";
      els.sourceEmpty.hidden = false;
      els.sourceContent.hidden = true;
      els.copyLink.hidden = true;
      els.copyPath.hidden = true;
      els.sourceClose.hidden = true;
      return;
    }

    els.sourceKicker.textContent = `${t("source")} · ${node.id}`;
    els.sourceTitle.textContent = node.title;
    els.sourceTitle.title = node.title;
    els.sourceLocation.textContent = `${node.source.path}:${node.source.targetLine}`;
    els.sourceLocation.title = `${node.source.path}:${node.source.targetLine}`;
    els.sourceEmpty.hidden = true;
    els.sourceContent.hidden = false;
    els.copyLink.hidden = false;
    els.copyPath.hidden = false;
    els.sourceClose.hidden = false;

    els.sourceStatus.replaceChildren();
    els.sourceStatus.appendChild(createElement("span", {
      className: `status-chip ${node.status}`,
      text: node.status === "verified" ? t("verified") : t("inferred")
    }));
    els.sourceStatus.appendChild(createElement("span", { className: "kind-chip", text: node.kind }));
    els.sourceStatus.appendChild(createElement("span", { className: "kind-chip", text: node.source.symbol, title: node.source.symbol }));

    const resolved = sourceSnapshot.nodes?.[node.id];
    const lines = Array.isArray(resolved?.lines) && resolved.lines.length
      ? resolved.lines
      : [node.source.snippet];
    const startLine = Number(resolved?.startLine || node.source.targetLine);
    const targetLine = Number(resolved?.targetLine || node.source.targetLine);
    els.sourceCode.replaceChildren();
    lines.forEach((line, index) => {
      const lineNumber = startLine + index;
      const row = createElement("li", {
        className: `code-row${lineNumber === targetLine ? " is-target" : ""}`,
        dataset: { lineNumber }
      });
      const code = createElement("code");
      appendHighlightedCode(code, line);
      row.appendChild(code);
      els.sourceCode.appendChild(row);
    });

    els.sourceRelations.replaceChildren();
    const relations = (data.edges || []).filter((edge) => edge.from === node.id || edge.to === node.id);
    if (!relations.length) {
      els.sourceRelations.appendChild(createElement("div", { className: "relation-card", text: "—" }));
    }
    for (const edge of relations) {
      const outgoing = edge.from === node.id;
      const otherId = outgoing ? edge.to : edge.from;
      const card = createElement("article", { className: `relation-card${edge.status === "inferred" ? " inferred" : ""}` });
      const line = createElement("div", { className: "relation-line" });
      line.appendChild(createElement("span", { text: outgoing ? t("outgoing") : t("incoming") }));
      line.appendChild(createElement("span", { text: "·" }));
      line.appendChild(createElement("span", { text: edge.label }));
      line.appendChild(createElement("span", { text: "·" }));
      const otherButton = createElement("button", {
        className: "relation-node-link",
        type: "button",
        text: otherId,
        attrs: { "aria-label": `${otherId}: ${nodeById.get(otherId)?.title || ""}` }
      });
      otherButton.addEventListener("click", () => selectNode(otherId, { focusPanel: false, scrollGuide: state.view === "guide" }));
      line.appendChild(otherButton);
      line.appendChild(createElement("span", {
        className: `status-chip ${edge.status}`,
        text: edge.status === "verified" ? t("verified") : t("inferred")
      }));
      card.appendChild(line);

      const evidence = edge.evidence?.[0];
      if (evidence) {
        card.appendChild(createElement("div", { className: "relation-claim", text: evidence.claim }));
        card.appendChild(createElement("div", {
          className: "relation-location",
          text: `${evidence.path}:${evidence.targetLine}`,
          title: `${evidence.path}:${evidence.targetLine}`
        }));
      }
      if (edge.reason) {
        const reason = createElement("div", { className: "relation-reason" });
        reason.appendChild(createElement("strong", { text: `${t("reason")}: ` }));
        reason.appendChild(document.createTextNode(edge.reason));
        card.appendChild(reason);
      }
      els.sourceRelations.appendChild(card);
    }
  }

  function updateSelectionClasses() {
    for (const [id, controls] of nodeControls.entries()) {
      for (const control of controls) {
        control.classList.toggle("is-selected", id === state.selectedNodeId);
        if (id === state.selectedNodeId) control.setAttribute("aria-current", "location");
        else control.removeAttribute("aria-current");
      }
    }
  }

  function isOverlayMode() {
    return overlayMedia.matches || data.presentation?.sourcePanel === "drawer";
  }

  function updatePanelMode() {
    const overlay = isOverlayMode();
    const forcedDrawer = data.presentation?.sourcePanel === "drawer";
    els.shell.classList.toggle("source-drawer-mode", forcedDrawer);
    document.body.classList.toggle("force-source-drawer", forcedDrawer);
    if (overlay) {
      els.sourcePanel.setAttribute("role", "dialog");
      els.sourcePanel.setAttribute("aria-modal", "true");
      const open = Boolean(state.selectedNodeId);
      els.sourcePanel.classList.toggle("is-open", open);
      els.sourceBackdrop.hidden = !open;
    } else {
      els.sourcePanel.setAttribute("role", "region");
      els.sourcePanel.removeAttribute("aria-modal");
      els.sourcePanel.classList.remove("is-open");
      els.sourceBackdrop.hidden = true;
    }
  }

  function selectNode(id, options = {}) {
    const node = nodeById.get(id);
    if (!node) return;
    if (document.activeElement instanceof HTMLElement && document.activeElement !== els.sourceClose) {
      state.lastFocused = document.activeElement;
    }
    state.selectedNodeId = id;
    const section = sectionById.get(node.sectionId);
    const details = sectionDetails.get(node.sectionId);
    if (section && details) details.open = true;
    updateSelectionClasses();
    renderSource(node);
    updatePanelMode();

    if (options.updateHash !== false && window.location.hash !== `#${id}`) {
      history.replaceState(null, "", `#${id}`);
    }

    if (options.scrollGuide && state.view === "guide") {
      const control = [...(nodeControls.get(id) || [])].find((candidate) => candidate.classList.contains("node-card"));
      control?.scrollIntoView({ block: "center", behavior: "smooth" });
    }

    if (isOverlayMode() || options.focusPanel) {
      window.requestAnimationFrame(() => els.sourceClose.focus({ preventScroll: true }));
    }
  }

  function clearSelection(options = {}) {
    state.selectedNodeId = null;
    updateSelectionClasses();
    renderSource(null);
    updatePanelMode();
    if (options.updateHash !== false && window.location.hash) {
      const cleanUrl = window.location.href.split("#", 1)[0];
      try {
        history.replaceState(null, "", cleanUrl);
      } catch (_) {
        window.location.hash = "";
      }
    }
    const restore = options.restoreFocus !== false ? state.lastFocused : null;
    state.lastFocused = null;
    if (restore && document.contains(restore)) restore.focus({ preventScroll: true });
  }

  function syncMapViewportHeight() {
    if (els.mapView.hidden) return;
    const top = Math.max(0, els.mapView.getBoundingClientRect().top);
    const available = Math.max(520, window.innerHeight - top);
    els.mapView.style.height = `${available}px`;
  }

  function setView(view) {
    state.view = view === "map" ? "map" : "guide";
    const map = state.view === "map";
    els.guideView.hidden = map;
    els.mapView.hidden = !map;
    els.guideButton.setAttribute("aria-pressed", String(!map));
    els.mapButton.setAttribute("aria-pressed", String(map));
    if (map) {
      syncMapViewportHeight();
      window.requestAnimationFrame(() => {
        resetMap();
      });
    }
  }

  function buildMap() {
    els.mapGroups.replaceChildren();
    els.mapNodes.replaceChildren();
    els.mapEdges.replaceChildren();
    mapLayout.clear();

    const groupOrderIds = data.presentation?.map?.groupOrder || (data.groups || []).map((group) => group.id);
    const groups = groupOrderIds.map((id) => groupById.get(id)).filter(Boolean);
    const remainingGroups = (data.groups || []).filter((group) => !groupOrderIds.includes(group.id));
    groups.push(...remainingGroups);

    const NODE_WIDTH = 196;
    const NODE_HEIGHT = 78;
    const GROUP_PAD_X = 30;
    const GROUP_PAD_TOP = 48;
    const GROUP_PAD_BOTTOM = 28;
    const NODE_GAP_X = 28;
    const NODE_GAP_Y = 20;
    const GROUP_GAP_X = 48;
    const GROUP_GAP_Y = 52;
    const MARGIN = 40;

    const groupBoxes = new Map();
    for (const group of groups) {
      const nodes = (data.nodes || [])
        .filter((node) => node.groupId === group.id)
        .sort((a, b) => (traceOrder.get(a.id) ?? 9999) - (traceOrder.get(b.id) ?? 9999));
      const columns = Math.min(3, Math.max(1, Math.ceil(Math.sqrt(nodes.length || 1))));
      const rows = Math.max(1, Math.ceil(nodes.length / columns));
      const width = GROUP_PAD_X * 2 + columns * NODE_WIDTH + Math.max(0, columns - 1) * NODE_GAP_X;
      const height = GROUP_PAD_TOP + rows * NODE_HEIGHT + Math.max(0, rows - 1) * NODE_GAP_Y + GROUP_PAD_BOTTOM;
      groupBoxes.set(group.id, { group, nodes, columns, rows, width, height, x: 0, y: 0 });
    }

    const featuredId = data.presentation?.map?.featuredGroupId;
    const groupColumns = data.presentation?.map?.direction === "TB"
      ? 1
      : Math.max(1, Math.min(4, Number(data.presentation?.map?.groupColumns || 2)));
    const rows = [];
    const featured = featuredId ? groupBoxes.get(featuredId) : null;
    if (featured) rows.push([featured]);
    const regular = groups
      .filter((group) => !featured || group.id !== featured.group.id)
      .map((group) => groupBoxes.get(group.id));
    for (let index = 0; index < regular.length; index += groupColumns) {
      rows.push(regular.slice(index, index + groupColumns));
    }
    if (!rows.length && groups.length) rows.push(groups.map((group) => groupBoxes.get(group.id)));

    const rowMetrics = rows.map((row) => ({
      width: row.reduce((sum, box) => sum + box.width, 0) + Math.max(0, row.length - 1) * GROUP_GAP_X,
      height: Math.max(...row.map((box) => box.height), 0)
    }));
    const worldWidth = Math.max(1100, ...rowMetrics.map((metric) => metric.width + MARGIN * 2));
    let y = MARGIN;
    rows.forEach((row, rowIndex) => {
      const metric = rowMetrics[rowIndex];
      let x = (worldWidth - metric.width) / 2;
      for (const box of row) {
        box.x = x;
        box.y = y;
        x += box.width + GROUP_GAP_X;
      }
      y += metric.height + GROUP_GAP_Y;
    });
    const worldHeight = Math.max(700, y - GROUP_GAP_Y + MARGIN);
    state.map.worldWidth = worldWidth;
    state.map.worldHeight = worldHeight;
    els.mapWorld.style.width = `${worldWidth}px`;
    els.mapWorld.style.height = `${worldHeight}px`;
    els.mapEdges.setAttribute("viewBox", `0 0 ${worldWidth} ${worldHeight}`);
    els.mapEdges.setAttribute("width", String(worldWidth));
    els.mapEdges.setAttribute("height", String(worldHeight));

    for (const box of groupBoxes.values()) {
      const groupElement = createElement("div", { className: `map-group tone-${box.group.tone || "gray"}` });
      groupElement.style.left = `${box.x}px`;
      groupElement.style.top = `${box.y}px`;
      groupElement.style.width = `${box.width}px`;
      groupElement.style.height = `${box.height}px`;
      groupElement.appendChild(createElement("div", { className: "map-group-label", text: box.group.label, title: box.group.label }));
      els.mapGroups.appendChild(groupElement);

      box.nodes.forEach((node, index) => {
        const row = Math.floor(index / box.columns);
        const position = index % box.columns;
        const column = row % 2 === 0 ? position : box.columns - 1 - position;
        const x = box.x + GROUP_PAD_X + column * (NODE_WIDTH + NODE_GAP_X);
        const yNode = box.y + GROUP_PAD_TOP + row * (NODE_HEIGHT + NODE_GAP_Y);
        mapLayout.set(node.id, { x, y: yNode, width: NODE_WIDTH, height: NODE_HEIGHT });

        const button = createElement("button", {
          className: `map-node-button${node.status === "inferred" ? " is-inferred" : ""}`,
          type: "button",
          id: `map-node-${node.id}`,
          dataset: { nodeId: node.id },
          attrs: { "aria-label": `${node.id}: ${node.title}, ${node.source.path}:${node.source.targetLine}` }
        });
        button.style.left = `${x}px`;
        button.style.top = `${yNode}px`;
        button.appendChild(createElement("span", { className: "map-node-id", text: node.id }));
        button.appendChild(createElement("span", { className: "map-node-title", text: node.title, title: node.title }));
        button.appendChild(createElement("span", { className: "map-node-kind", text: node.kind }));
        button.appendChild(createElement("span", {
          className: "map-node-path",
          text: `${node.source.path}:${node.source.targetLine}`,
          title: `${node.source.path}:${node.source.targetLine}`
        }));
        button.addEventListener("click", () => selectNode(node.id, { focusPanel: state.keyboardInput, scrollGuide: false }));
        registerNodeControl(node.id, button);
        els.mapNodes.appendChild(button);
      });
    }

    const defs = createSvgElement("defs");
    const marker = createSvgElement("marker", {
      id: "edge-arrow",
      markerWidth: 8,
      markerHeight: 8,
      refX: 7,
      refY: 4,
      orient: "auto",
      markerUnits: "strokeWidth"
    });
    marker.appendChild(createSvgElement("path", { d: "M0,0 L8,4 L0,8 z", fill: "currentColor" }));
    defs.appendChild(marker);
    els.mapEdges.appendChild(defs);

    for (const edge of data.edges || []) {
      const from = mapLayout.get(edge.from);
      const to = mapLayout.get(edge.to);
      if (!from || !to) continue;
      const geometry = edgeGeometry(from, to);
      const path = createSvgElement("path", {
        d: geometry.path,
        class: `edge-path kind-${edge.kind}${edge.status === "inferred" ? " is-inferred" : ""}`
      });
      els.mapEdges.appendChild(path);

      const edgeLabel = String(edge.label ?? "");
      const labelWidth = Math.max(48, Math.min(132, edgeLabel.length * 6.2 + 16));
      const labelGroup = createSvgElement("g");
      labelGroup.appendChild(createSvgElement("rect", {
        x: geometry.labelX - labelWidth / 2,
        y: geometry.labelY - 9,
        width: labelWidth,
        height: 18,
        rx: 6,
        class: "edge-label-bg"
      }));
      const label = createSvgElement("text", {
        x: geometry.labelX,
        y: geometry.labelY + 0.5,
        class: "edge-label"
      });
      label.textContent = edgeLabel;
      labelGroup.appendChild(label);
      els.mapEdges.appendChild(labelGroup);
    }

    updateSelectionClasses();
  }

  function edgeGeometry(from, to) {
    const fromCenterX = from.x + from.width / 2;
    const fromCenterY = from.y + from.height / 2;
    const toCenterX = to.x + to.width / 2;
    const toCenterY = to.y + to.height / 2;

    if (to.x >= from.x + from.width + 18) {
      const startX = from.x + from.width;
      const startY = fromCenterY;
      const endX = to.x;
      const endY = toCenterY;
      const bend = Math.max(48, (endX - startX) * 0.48);
      return {
        path: `M ${startX} ${startY} C ${startX + bend} ${startY}, ${endX - bend} ${endY}, ${endX} ${endY}`,
        labelX: (startX + endX) / 2,
        labelY: (startY + endY) / 2 - 12
      };
    }

    const downward = toCenterY >= fromCenterY;
    const startX = fromCenterX;
    const startY = downward ? from.y + from.height : from.y;
    const endX = toCenterX;
    const endY = downward ? to.y : to.y + to.height;
    const direction = downward ? 1 : -1;
    const bend = Math.max(44, Math.abs(endY - startY) * 0.48);
    return {
      path: `M ${startX} ${startY} C ${startX} ${startY + direction * bend}, ${endX} ${endY - direction * bend}, ${endX} ${endY}`,
      labelX: (startX + endX) / 2,
      labelY: (startY + endY) / 2 - 11
    };
  }

  function applyMapTransform() {
    const { x, y, scale } = state.map;
    els.mapWorld.style.transform = `translate(${x}px, ${y}px) scale(${scale})`;
  }

  function resetMap() {
    const rect = els.mapStage.getBoundingClientRect();
    if (!rect.width || !rect.height) return;
    const padding = 44;
    const scale = Math.max(0.2, Math.min(1, (rect.width - padding * 2) / state.map.worldWidth, (rect.height - padding * 2) / state.map.worldHeight));
    state.map.scale = scale;
    state.map.x = (rect.width - state.map.worldWidth * scale) / 2;
    state.map.y = (rect.height - state.map.worldHeight * scale) / 2;
    state.mapInitialized = true;
    applyMapTransform();
  }

  function zoomMap(factor, originX, originY) {
    const rect = els.mapStage.getBoundingClientRect();
    const px = originX ?? rect.width / 2;
    const py = originY ?? rect.height / 2;
    const oldScale = state.map.scale;
    const newScale = Math.max(0.2, Math.min(2.2, oldScale * factor));
    const worldX = (px - state.map.x) / oldScale;
    const worldY = (py - state.map.y) / oldScale;
    state.map.scale = newScale;
    state.map.x = px - worldX * newScale;
    state.map.y = py - worldY * newScale;
    applyMapTransform();
  }

  function initializeMapInteractions() {
    els.zoomIn.addEventListener("click", () => zoomMap(1.18));
    els.zoomOut.addEventListener("click", () => zoomMap(1 / 1.18));
    els.resetMap.addEventListener("click", resetMap);

    els.mapStage.addEventListener("wheel", (event) => {
      event.preventDefault();
      const rect = els.mapStage.getBoundingClientRect();
      zoomMap(event.deltaY < 0 ? 1.11 : 1 / 1.11, event.clientX - rect.left, event.clientY - rect.top);
    }, { passive: false });

    els.mapStage.addEventListener("pointerdown", (event) => {
      if (event.target.closest("button")) return;
      els.mapStage.setPointerCapture(event.pointerId);
      state.pan = { pointerId: event.pointerId, startX: event.clientX, startY: event.clientY, mapX: state.map.x, mapY: state.map.y };
      els.mapStage.classList.add("is-panning");
    });
    els.mapStage.addEventListener("pointermove", (event) => {
      if (!state.pan || state.pan.pointerId !== event.pointerId) return;
      state.map.x = state.pan.mapX + event.clientX - state.pan.startX;
      state.map.y = state.pan.mapY + event.clientY - state.pan.startY;
      applyMapTransform();
    });
    const endPan = (event) => {
      if (!state.pan || state.pan.pointerId !== event.pointerId) return;
      state.pan = null;
      els.mapStage.classList.remove("is-panning");
    };
    els.mapStage.addEventListener("pointerup", endPan);
    els.mapStage.addEventListener("pointercancel", endPan);

    els.mapStage.addEventListener("keydown", (event) => {
      const step = event.shiftKey ? 70 : 28;
      if (event.key === "ArrowLeft") state.map.x += step;
      else if (event.key === "ArrowRight") state.map.x -= step;
      else if (event.key === "ArrowUp") state.map.y += step;
      else if (event.key === "ArrowDown") state.map.y -= step;
      else if (event.key === "+" || event.key === "=") zoomMap(1.15);
      else if (event.key === "-") zoomMap(1 / 1.15);
      else if (event.key === "0") resetMap();
      else return;
      event.preventDefault();
      applyMapTransform();
    });

    if ("ResizeObserver" in window) {
      const observer = new ResizeObserver(() => {
        if (state.view === "map" && !state.pan) resetMap();
      });
      observer.observe(els.mapStage);
    }
  }

  function applyTheme(theme) {
    state.theme = theme;
    document.documentElement.dataset.theme = theme;
    saveTheme(theme);
    const label = theme === "light" ? t("themeLight") : theme === "dark" ? t("themeDark") : t("themeSystem");
    els.themeButton.setAttribute("aria-label", label);
    els.themeButton.title = label;
  }

  function cycleTheme() {
    const themes = ["system", "light", "dark"];
    const index = themes.indexOf(state.theme);
    applyTheme(themes[(index + 1) % themes.length]);
  }

  async function copyText(text, successMessage) {
    const value = String(text ?? "");
    try {
      if (!navigator.clipboard?.writeText) {
        throw new Error("Clipboard API unavailable");
      }
      await navigator.clipboard.writeText(value);
      showToast(successMessage);
    } catch (_) {
      const fallback = window.prompt("Clipboard unavailable. Copy this value manually:", value);
      showToast(fallback === null ? t("copyFailed") : successMessage);
    }
  }

  function showToast(message) {
    window.clearTimeout(state.toastTimer);
    els.toast.textContent = message;
    els.toast.classList.add("is-visible");
    state.toastTimer = window.setTimeout(() => els.toast.classList.remove("is-visible"), 1800);
  }

  function currentNode() {
    return state.selectedNodeId ? nodeById.get(state.selectedNodeId) : null;
  }

  function focusableInPanel() {
    return [...els.sourcePanel.querySelectorAll("button:not([hidden]), [href], [tabindex]:not([tabindex='-1'])")]
      .filter((element) => !element.disabled && element.getClientRects().length > 0);
  }

  function initializeEvents() {
    els.guideLabel.textContent = t("guide");
    els.mapLabel.textContent = t("map");
    els.guideButton.setAttribute("aria-label", t("guide"));
    els.mapButton.setAttribute("aria-label", t("map"));
    els.themeButton.setAttribute("aria-label", t("theme"));
    els.themeButton.title = t("theme");
    els.zoomIn.setAttribute("aria-label", t("zoomIn"));
    els.zoomIn.title = t("zoomIn");
    els.zoomOut.setAttribute("aria-label", t("zoomOut"));
    els.zoomOut.title = t("zoomOut");
    els.resetMap.setAttribute("aria-label", t("resetMap"));
    els.resetMap.title = t("resetMap");
    els.mapStage.setAttribute("aria-label", t("interactiveMap"));
    els.sourceKicker.textContent = t("source");
    els.sourceTitle.textContent = t("selectNode");
    els.sourceEmptyText.textContent = t("sourceEmpty");
    els.relationsTitle.textContent = t("relations");
    els.copyLink.setAttribute("aria-label", t("copyLink"));
    els.copyLink.title = t("copyLink");
    els.copyPath.setAttribute("aria-label", t("copyPath"));
    els.copyPath.title = t("copyPath");
    els.sourceClose.setAttribute("aria-label", t("closeSource"));
    els.sourceClose.title = t("closeSource");
    els.sourceBackdrop.setAttribute("aria-label", t("closeSource"));

    els.guideButton.addEventListener("click", () => setView("guide"));
    els.mapButton.addEventListener("click", () => setView("map"));
    els.themeButton.addEventListener("click", cycleTheme);
    els.sourceClose.addEventListener("click", () => clearSelection());
    els.sourceBackdrop.addEventListener("click", () => clearSelection());
    els.copyLink.addEventListener("click", () => {
      const node = currentNode();
      if (!node) return;
      const url = new URL(window.location.href);
      url.hash = node.id;
      copyText(url.toString(), t("copiedLink"));
    });
    els.copyPath.addEventListener("click", () => {
      const node = currentNode();
      if (!node) return;
      copyText(`${node.source.path}:${node.source.targetLine}`, t("copiedPath"));
    });

    document.addEventListener("keydown", (event) => {
      if (["Tab", "Enter", " ", "ArrowUp", "ArrowDown", "ArrowLeft", "ArrowRight"].includes(event.key)) state.keyboardInput = true;
      if (event.key === "Escape" && state.selectedNodeId) {
        event.preventDefault();
        clearSelection();
        return;
      }
      if (event.key === "Tab" && isOverlayMode() && state.selectedNodeId) {
        const focusable = focusableInPanel();
        if (!focusable.length) return;
        const first = focusable[0];
        const last = focusable[focusable.length - 1];
        if (event.shiftKey && document.activeElement === first) {
          event.preventDefault();
          last.focus();
        } else if (!event.shiftKey && document.activeElement === last) {
          event.preventDefault();
          first.focus();
        }
      }
    }, true);
    document.addEventListener("pointerdown", () => { state.keyboardInput = false; }, true);
    window.addEventListener("hashchange", handleHash);
    window.addEventListener("resize", () => {
      if (state.view !== "map") return;
      syncMapViewportHeight();
      resetMap();
    });
    overlayMedia.addEventListener?.("change", updatePanelMode);
  }

  function handleHash() {
    const id = decodeURIComponent(window.location.hash.replace(/^#/, ""));
    if (id && nodeById.has(id)) selectNode(id, { updateHash: false, focusPanel: false, scrollGuide: state.view === "guide" });
    else if (!id && state.selectedNodeId) clearSelection({ updateHash: false, restoreFocus: false });
  }

  function initialize() {
    initializeIcons();
    renderMetadata();
    renderSections();
    buildMap();
    initializeMapInteractions();
    initializeEvents();
    applyTheme(state.theme);
    renderSource(null);
    updatePanelMode();
    setView(state.view);
    handleHash();
  }

  initialize();
})();
