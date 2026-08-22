// CSRF protection: every plain <form method="post"> gets a hidden
// csrf_token field injected right before it submits, sourced from the
// <meta name="csrf-token"> tag layout.html renders. Centralized here so
// individual page templates don't each need a hidden input by hand - and
// since this runs on the "submit" event (not DOMContentLoaded), it also
// covers forms that get submitted programmatically via requestSubmit()
// after the custom confirm() modal below.
(function () {
	document.addEventListener("submit", (e) => {
		const form = e.target;
		if (!(form instanceof HTMLFormElement)) return;
		if ((form.getAttribute("method") || "get").toLowerCase() !== "post") return;
		if (form.querySelector('input[name="csrf_token"]')) return;
		const meta = document.querySelector('meta[name="csrf-token"]');
		if (!meta) return;
		const input = document.createElement("input");
		input.type = "hidden";
		input.name = "csrf_token";
		input.value = meta.content;
		form.appendChild(input);
	});
})();

// Update-available banner (layout.html): dismissing it hides it for the
// rest of this browser until a newer version ships, at which point the
// stored version no longer matches data-dismiss-version and it reappears.
(function () {
	const banner = document.getElementById("update-banner");
	if (!banner) return;
	try {
		if (localStorage.getItem("sd-update-dismissed") === banner.dataset.dismissVersion) {
			banner.remove();
			return;
		}
	} catch (e) {}
	const dismiss = document.getElementById("update-banner-dismiss");
	if (dismiss) {
		dismiss.addEventListener("click", () => {
			try { localStorage.setItem("sd-update-dismissed", banner.dataset.dismissVersion); } catch (e) {}
			banner.remove();
		});
	}
})();

// Node picker: auto-submits its <form> on change (volumes/images pages'
// node <select>), replacing an inline onchange= that CSP's script-src
// blocks (nonces only cover <script> elements, not event-handler attrs).
(function () {
	document.querySelectorAll("select[data-auto-submit]").forEach((select) => {
		select.addEventListener("change", () => select.form.submit());
	});
})();

// Reset-password forms (users.html): prompts for a new password client-side
// via data-prompt-password="<username>" instead of an inline onsubmit=,
// then fills the hidden password field before the form submits.
(function () {
	document.querySelectorAll("form[data-prompt-password]").forEach((form) => {
		form.addEventListener("submit", (e) => {
			const p = prompt("New password for " + form.dataset.promptPassword + ":");
			if (!p) { e.preventDefault(); return; }
			form.password.value = p;
		});
	});
})();

// Custom file picker (see .file-picker in app.css): keeps the visible
// filename text in sync with the visually-hidden native <input type=file>.
(function () {
	document.querySelectorAll(".file-picker input[type=file]").forEach((input) => {
		const nameEl = input.parentElement.querySelector(".file-picker-name");
		if (!nameEl) return;
		const defaultText = nameEl.textContent;
		input.addEventListener("change", () => {
			nameEl.textContent = input.files.length ? input.files[0].name : defaultText;
		});
	});
})();

// Lightweight, dependency-free search + sort for any <table data-enhance>.
// A text input with [data-table-search] anywhere inside the same
// `.table-wrap` filters rows by substring match across all cells;
// clicking a header cell sorts by that column (numeric-aware).
(function () {
	function enhanceTable(table) {
		const wrap = table.closest(".table-wrap") || table.parentElement;
		const search = wrap.querySelector("[data-table-search]");
		const tbody = table.tBodies[0];
		if (!tbody) return;

		if (search) {
			search.addEventListener("input", () => {
				const q = search.value.toLowerCase();
				for (const row of tbody.rows) {
					row.style.display = row.textContent.toLowerCase().includes(q) ? "" : "none";
				}
			});
		}

		if (!table.tHead) return;
		Array.from(table.tHead.rows[0].cells).forEach((th, idx) => {
			if (th.dataset.noSort !== undefined) return;
			th.style.cursor = "pointer";
			th.title = "Click to sort";
			let asc = true;
			th.addEventListener("click", () => {
				const rows = Array.from(tbody.rows);
				rows.sort((a, b) => {
					const av = (a.cells[idx] && a.cells[idx].textContent.trim()) || "";
					const bv = (b.cells[idx] && b.cells[idx].textContent.trim()) || "";
					const an = parseFloat(av);
					const bn = parseFloat(bv);
					const bothNumeric = !isNaN(an) && !isNaN(bn) && /^-?[0-9.]+/.test(av) && /^-?[0-9.]+/.test(bv);
					const cmp = bothNumeric ? an - bn : av.localeCompare(bv);
					return asc ? cmp : -cmp;
				});
				asc = !asc;
				rows.forEach((r) => tbody.appendChild(r));
			});
		});
	}

	document.addEventListener("DOMContentLoaded", () => {
		document.querySelectorAll("table[data-enhance]").forEach(enhanceTable);
	});
})();

// Active-link highlighting: marks the sidebar link whose href is the
// longest match (exact or path-prefix) of the current pathname, so e.g.
// /stacks/gitops highlights "GitOps deploy" rather than the broader
// "Stacks" link, while /stacks/mystack (a stack detail page, not itself
// in the nav) still falls back to highlighting "Stacks". Also positions a
// floating pill (.nav-pill) behind the active link and animates it into
// place, so navigation reads as a single moving highlight rather than a
// static background swap.
(function () {
	const path = window.location.pathname;
	let best = null;
	document.querySelectorAll(".sidebar a[href]").forEach((a) => {
		const href = a.getAttribute("href");
		const isMatch = href === "/" ? path === "/" : (path === href || path.startsWith(href + "/"));
		if (isMatch && (!best || href.length > best.href.length)) best = { a, href };
	});
	if (!best) return;
	best.a.classList.add("active");

	const navScroll = document.querySelector(".nav-scroll");
	if (!navScroll) return;
	const pill = document.createElement("div");
	pill.className = "nav-pill";
	navScroll.prepend(pill);

	function place() {
		pill.style.transform = `translateY(${best.a.offsetTop}px)`;
		pill.style.height = best.a.offsetHeight + "px";
		pill.style.opacity = "1";
	}
	requestAnimationFrame(place);
	window.addEventListener("resize", place);
})();

// Theme toggle: overrides the OS-level prefers-color-scheme via a
// `data-theme` attribute on <html>, persisted so it survives navigation
// (every page is a full document load, not an SPA route). A tiny inline
// script in <head> applies the stored value before first paint to avoid a
// flash of the wrong theme.
(function () {
	const btn = document.getElementById("theme-toggle");
	if (!btn) return;
	btn.addEventListener("click", () => {
		const root = document.documentElement;
		const current = root.dataset.theme ||
			(window.matchMedia("(prefers-color-scheme: light)").matches ? "light" : "dark");
		const next = current === "light" ? "dark" : "light";
		root.dataset.theme = next;
		try { localStorage.setItem("sd-theme", next); } catch (e) {}
	});
})();

// Command palette (Cmd/Ctrl+K): builds its list of jump targets directly
// from the sidebar's own <a> elements, so it never drifts out of sync with
// the nav and needs no server-side search endpoint.
(function () {
	const overlay = document.getElementById("cmdk-overlay");
	if (!overlay) return;
	const input = document.getElementById("cmdk-input");
	const list = document.getElementById("cmdk-list");
	let items = [];
	let active = 0;
	let lastFocus = null;

	function buildItems() {
		const groups = [];
		let current = null;
		document.querySelectorAll(".sidebar .nav-scroll > *").forEach((el) => {
			if (el.classList.contains("nav-group-label")) {
				current = { label: el.textContent, links: [] };
				groups.push(current);
			} else if (el.tagName === "A" && current) {
				current.links.push(el);
			}
		});
		return groups;
	}

	function render(query) {
		const q = query.trim().toLowerCase();
		const groups = buildItems();
		list.innerHTML = "";
		items = [];
		groups.forEach((group) => {
			const matches = group.links.filter((a) => a.textContent.toLowerCase().includes(q));
			if (!matches.length) return;
			const label = document.createElement("div");
			label.className = "cmdk-group-label";
			label.textContent = group.label;
			list.appendChild(label);
			matches.forEach((a) => {
				const row = document.createElement("div");
				row.className = "cmdk-item";
				row.setAttribute("role", "option");
				row.innerHTML = (a.querySelector("svg") ? a.querySelector("svg").outerHTML : "") +
					"<span>" + a.textContent + "</span>";
				row.addEventListener("mouseenter", () => setActive(items.indexOf(row)));
				row.addEventListener("click", () => { window.location.href = a.getAttribute("href"); });
				list.appendChild(row);
				items.push(row);
				row.dataset.href = a.getAttribute("href");
			});
		});
		if (!items.length) {
			const empty = document.createElement("div");
			empty.className = "cmdk-empty";
			empty.textContent = "No matching pages.";
			list.appendChild(empty);
		}
		setActive(0);
	}

	function setActive(idx) {
		if (!items.length) return;
		active = (idx + items.length) % items.length;
		items.forEach((it, i) => it.classList.toggle("active", i === active));
		items[active].scrollIntoView({ block: "nearest" });
	}

	function open() {
		lastFocus = document.activeElement;
		overlay.classList.add("open");
		render("");
		input.value = "";
		setTimeout(() => input.focus(), 10);
		document.body.style.overflow = "hidden";
	}

	function close() {
		overlay.classList.remove("open");
		document.body.style.overflow = "";
		if (lastFocus && lastFocus.focus) lastFocus.focus();
	}

	document.getElementById("cmdk-sidebar-trigger") && document.getElementById("cmdk-sidebar-trigger").addEventListener("click", open);
	document.getElementById("cmdk-topbar-trigger") && document.getElementById("cmdk-topbar-trigger").addEventListener("click", open);
	overlay.addEventListener("mousedown", (e) => { if (e.target === overlay) close(); });

	input.addEventListener("input", () => render(input.value));
	input.addEventListener("keydown", (e) => {
		if (e.key === "ArrowDown") { e.preventDefault(); setActive(active + 1); }
		else if (e.key === "ArrowUp") { e.preventDefault(); setActive(active - 1); }
		else if (e.key === "Enter") { e.preventDefault(); if (items[active]) window.location.href = items[active].dataset.href; }
		else if (e.key === "Escape") { e.preventDefault(); close(); }
	});

	document.addEventListener("keydown", (e) => {
		if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === "k") {
			e.preventDefault();
			overlay.classList.contains("open") ? close() : open();
		} else if (e.key === "Escape" && overlay.classList.contains("open")) {
			close();
		}
	});
})();

// Custom confirm modal: replaces the native confirm() dialog for plain
// <form method="post"> actions that opt in via data-confirm="...". Message
// text is already fully rendered server-side for every case except the
// services bulk-action form, whose message depends on the selected action;
// that one case is handled via a {value} placeholder resolved from a named
// form field (data-confirm-field).
(function () {
	const overlay = document.getElementById("confirm-overlay");
	if (!overlay) return;
	const messageEl = document.getElementById("confirm-message");
	const okBtn = document.getElementById("confirm-ok");
	const cancelBtn = document.getElementById("confirm-cancel");
	let pendingForm = null;
	let lastFocus = null;

	function open(message, form) {
		pendingForm = form;
		lastFocus = document.activeElement;
		messageEl.textContent = message;
		overlay.classList.add("open");
		document.body.style.overflow = "hidden";
		setTimeout(() => okBtn.focus(), 10);
	}

	function close() {
		overlay.classList.remove("open");
		document.body.style.overflow = "";
		pendingForm = null;
		if (lastFocus && lastFocus.focus) lastFocus.focus();
	}

	document.querySelectorAll("form[data-confirm]").forEach((form) => {
		form.addEventListener("submit", (e) => {
			if (form._confirmed) { form._confirmed = false; return; }
			e.preventDefault();
			let message = form.dataset.confirm;
			const fieldName = form.dataset.confirmField;
			if (fieldName && form.elements[fieldName]) {
				message = message.replace("{value}", form.elements[fieldName].value);
			}
			open(message, form);
		});
	});

	okBtn.addEventListener("click", () => {
		const form = pendingForm;
		close();
		if (form) { form._confirmed = true; form.requestSubmit(); }
	});
	cancelBtn.addEventListener("click", close);
	overlay.addEventListener("mousedown", (e) => { if (e.target === overlay) close(); });
	document.addEventListener("keydown", (e) => {
		if (e.key === "Escape" && overlay.classList.contains("open")) close();
	});
})();

// Staggered entrance for freshly-rendered table rows and bento tiles, so a
// page load reads as content settling into place rather than popping in
// all at once. Capped so long tables don't produce a multi-second cascade.
(function () {
	if (window.matchMedia("(prefers-reduced-motion: reduce)").matches) return;
	document.querySelectorAll("table tbody tr").forEach((row, i) => {
		if (i > 24) return;
		row.style.animationDelay = Math.min(i * 16, 260) + "ms";
		row.classList.add("enter");
	});
	document.querySelectorAll(".stat-card, .ring-card").forEach((card, i) => {
		card.style.animationDelay = Math.min(i * 45, 220) + "ms";
	});
})();

// Async node resource usage: the nodes list and node detail pages render
// immediately from cheap Docker API data (see handlers_nodes.go) and fetch
// real CPU/memory usage from the agent-backed /nodes/stats endpoints
// afterwards, since fanning out to every node's agent takes a couple of
// seconds - each container needs two stats samples a second apart to
// compute a CPU rate (see containerResourceUsage in agent/stats.go) - and
// that shouldn't block the page from rendering.
(function () {
	function levelClass(pct) {
		if (pct >= 85) return "crit";
		if (pct >= 60) return "warn";
		return "ok";
	}
	function bytesHuman(b) {
		const units = "KMGTPE";
		if (b < 1024) return Math.round(b) + " B";
		let div = 1024, exp = 0;
		for (let n = Math.floor(b / 1024); n >= 1024; n = Math.floor(n / 1024)) { div *= 1024; exp++; }
		return (b / div).toFixed(1) + " " + units[exp] + "iB";
	}

	function fillCell(cell, ok, pct, extra) {
		if (!cell) return;
		cell.classList.remove("node-stat-loading");
		cell.innerHTML = ok
			? '<span class="usage-' + levelClass(pct) + '">' + Math.round(pct) + '%</span> <span class="muted">(' + extra + ')</span>'
			: '<span class="muted">—</span>';
	}

	const cpuCells = document.querySelectorAll("[id^='cpu-used-']");
	if (cpuCells.length) {
		fetch("/nodes/stats")
			.then((r) => (r.ok ? r.json() : {}))
			.then((stats) => {
				cpuCells.forEach((cpuCell) => {
					const id = cpuCell.id.slice("cpu-used-".length);
					const s = stats[id];
					fillCell(cpuCell, s && s.ok, s && s.cpuUsedPct, s ? s.cpuUsedCores.toFixed(2) : "");
					fillCell(document.getElementById("mem-used-" + id), s && s.ok, s && s.memUsedPct, s ? bytesHuman(s.memUsedBytes) : "");
					fillCell(document.getElementById("disk-used-" + id), s && s.ok && s.diskTotalBytes > 0, s && s.diskUsedPct, s ? bytesHuman(s.diskUsedBytes) : "");
				});
			})
			.catch(() => {
				cpuCells.forEach((cpuCell) => {
					const id = cpuCell.id.slice("cpu-used-".length);
					fillCell(cpuCell, false);
					fillCell(document.getElementById("mem-used-" + id), false);
					fillCell(document.getElementById("disk-used-" + id), false);
				});
			});
	}

	const usageEl = document.getElementById("node-usage");
	if (usageEl) {
		const nodeID = usageEl.dataset.nodeId;
		const cpuCapacity = usageEl.dataset.cpuCapacity;
		const memCapacity = usageEl.dataset.memCapacity;
		fetch("/nodes/" + encodeURIComponent(nodeID) + "/stats")
			.then((r) => (r.ok ? r.json() : null))
			.then((s) => {
				if (!s || !s.ok) {
					usageEl.innerHTML = '<p class="muted">Live CPU/memory usage unavailable — could not reach this node\'s agent.</p>';
					return;
				}
				const diskRing = s.diskTotalBytes > 0
					? '<div class="ring-card">' +
						'<div class="ring ' + levelClass(s.diskUsedPct) + '" style="--pct:' + s.diskUsedPct + '"><div class="ring-pct">' + Math.round(s.diskUsedPct) + '%</div></div>' +
						'<div class="ring-info"><div class="l">Disk used</div><div class="n">' + bytesHuman(s.diskUsedBytes) + ' <span class="muted">of ' + bytesHuman(s.diskTotalBytes) + '</span></div></div>' +
					'</div>'
					: '';
				usageEl.innerHTML =
					'<div class="ring-grid">' +
						'<div class="ring-card">' +
							'<div class="ring ' + levelClass(s.cpuUsedPct) + '" style="--pct:' + s.cpuUsedPct + '"><div class="ring-pct">' + Math.round(s.cpuUsedPct) + '%</div></div>' +
							'<div class="ring-info"><div class="l">CPU used</div><div class="n">' + s.cpuUsedCores.toFixed(2) + ' <span class="muted">of ' + cpuCapacity + ' vCPU</span></div></div>' +
						'</div>' +
						'<div class="ring-card">' +
							'<div class="ring ' + levelClass(s.memUsedPct) + '" style="--pct:' + s.memUsedPct + '"><div class="ring-pct">' + Math.round(s.memUsedPct) + '%</div></div>' +
							'<div class="ring-info"><div class="l">Memory used</div><div class="n">' + bytesHuman(s.memUsedBytes) + ' <span class="muted">of ' + memCapacity + '</span></div></div>' +
						'</div>' +
						diskRing +
					'</div>';
			})
			.catch(() => {
				usageEl.innerHTML = '<p class="muted">Live CPU/memory usage unavailable.</p>';
			});
	}
})();

// Mobile navigation: off-canvas sidebar toggled by the hamburger button in
// the topbar (topbar/sidebar are only visible <=880px, see app.css).
(function () {
	const toggle = document.getElementById("nav-toggle");
	const shell = document.querySelector(".shell");
	const backdrop = document.getElementById("sidebar-backdrop");
	if (!toggle || !shell) return;

	function close() {
		shell.classList.remove("nav-open");
		toggle.setAttribute("aria-expanded", "false");
	}
	function open() {
		shell.classList.add("nav-open");
		toggle.setAttribute("aria-expanded", "true");
	}

	toggle.addEventListener("click", () => {
		shell.classList.contains("nav-open") ? close() : open();
	});
	if (backdrop) backdrop.addEventListener("click", close);
	document.querySelectorAll(".sidebar a, .sidebar button[type=submit]").forEach((el) => {
		el.addEventListener("click", close);
	});
	document.addEventListener("keydown", (e) => {
		if (e.key === "Escape") close();
	});
})();

// Portainer-style repeatable-row editors for `<textarea data-list-editor>`.
// The backend (handlers_service_spec.go) still round-trips these fields as
// plain newline-delimited strings (parseLines/parseKVLines etc.), so rather
// than touch that contract this only changes how the value is *edited*: the
// textarea is hidden and a row of real inputs per entry takes its place,
// syncing back into the textarea's value on every change and right before
// submit. With JS disabled the plain textarea still works as before.
(function () {
	const MODES = {
		// One arg/name per row: entrypoint, command, networks, constraints,
		// spread preferences.
		line: {
			fields: [{ key: "value", placeholder: "" }],
			parse: (line) => ({ value: line }),
			format: (row) => row.value.trim(),
		},
		// KEY=VALUE per row: env vars, labels.
		kv: {
			fields: [
				{ key: "key", placeholder: "KEY", flex: "1" },
				{ key: "value", placeholder: "value", flex: "2" },
			],
			parse: (line) => {
				const i = line.indexOf("=");
				return i === -1 ? { key: line, value: "" } : { key: line.slice(0, i), value: line.slice(i + 1) };
			},
			format: (row) => (row.key.trim() ? row.key.trim() + "=" + row.value : ""),
		},
		// name[:target-filename] per row: secrets, configs.
		ref: {
			fields: [
				{ key: "name", placeholder: "name", flex: "1" },
				{ key: "target", placeholder: "target filename (optional)", flex: "1" },
			],
			parse: (line) => {
				const i = line.indexOf(":");
				return i === -1 ? { name: line, target: "" } : { name: line.slice(0, i), target: line.slice(i + 1) };
			},
			format: (row) => (row.name.trim() ? row.name.trim() + (row.target.trim() ? ":" + row.target.trim() : "") : ""),
		},
		// source:target[:ro] per row: mounts.
		mount: {
			fields: [
				{ key: "source", placeholder: "/host/path or volume name", flex: "2" },
				{ key: "target", placeholder: "/container/path", flex: "2" },
				{ key: "ro", type: "checkbox", label: "Read-only" },
			],
			parse: (line) => {
				const parts = line.split(":");
				const ro = parts.length > 2 && parts[parts.length - 1] === "ro";
				if (ro) parts.pop();
				return { source: parts[0] || "", target: parts.slice(1).join(":") || "", ro };
			},
			format: (row) => (row.source.trim() && row.target.trim() ? row.source.trim() + ":" + row.target.trim() + (row.ro ? ":ro" : "") : ""),
		},
		// published:target[/udp] per row: ports.
		port: {
			fields: [
				{ key: "published", placeholder: "8080", flex: "0 0 90px" },
				{ key: "target", placeholder: "80", flex: "0 0 90px" },
				{ key: "protocol", type: "select", options: ["tcp", "udp"], flex: "0 0 84px" },
			],
			parse: (line) => {
				const [portion, proto] = line.split("/");
				const [published, target] = portion.split(":");
				return { published: published || "", target: target || "", protocol: proto || "tcp" };
			},
			format: (row) => (row.published.trim() && row.target.trim() ? row.published.trim() + ":" + row.target.trim() + (row.protocol === "udp" ? "/udp" : "") : ""),
		},
	};

	function buildRow(fields, mode, values, onChange) {
		const row = document.createElement("div");
		row.className = "list-row";
		const state = Object.assign({}, values);
		fields.forEach((f) => {
			if (state[f.key] === undefined) state[f.key] = f.type === "checkbox" ? false : f.type === "select" ? f.options[0] : "";
		});

		fields.forEach((f) => {
			if (f.type === "checkbox") {
				const label = document.createElement("label");
				label.className = "list-row-check";
				const input = document.createElement("input");
				input.type = "checkbox";
				input.checked = !!state[f.key];
				input.addEventListener("change", () => { state[f.key] = input.checked; onChange(); });
				label.appendChild(input);
				label.appendChild(document.createTextNode(f.label));
				row.appendChild(label);
			} else if (f.type === "select") {
				const select = document.createElement("select");
				if (f.flex) select.style.flex = f.flex;
				f.options.forEach((opt) => {
					const o = document.createElement("option");
					o.value = opt;
					o.textContent = opt;
					if (state[f.key] === opt) o.selected = true;
					select.appendChild(o);
				});
				select.addEventListener("change", () => { state[f.key] = select.value; onChange(); });
				row.appendChild(select);
			} else {
				const input = document.createElement("input");
				input.type = "text";
				input.className = "mono";
				input.placeholder = f.placeholder || "";
				input.value = state[f.key] || "";
				if (f.flex) input.style.flex = f.flex;
				input.addEventListener("input", () => { state[f.key] = input.value; onChange(); });
				row.appendChild(input);
			}
		});

		const removeBtn = document.createElement("button");
		removeBtn.type = "button";
		removeBtn.className = "list-row-remove";
		removeBtn.setAttribute("aria-label", "Remove");
		removeBtn.textContent = "✕";
		removeBtn.addEventListener("click", () => { row.remove(); onChange(); });
		row.appendChild(removeBtn);

		row._getState = () => state;
		return row;
	}

	function initEditor(textarea) {
		const mode = MODES[textarea.dataset.listEditor];
		if (!mode) return;

		const overrides = textarea.dataset.placeholders ? textarea.dataset.placeholders.split("|") : [];
		const fields = mode.fields.map((f, i) => (overrides[i] ? Object.assign({}, f, { placeholder: overrides[i] }) : f));

		const wrap = document.createElement("div");
		wrap.className = "list-editor";
		textarea.hidden = true;
		textarea.insertAdjacentElement("afterend", wrap);

		const rowsEl = document.createElement("div");
		rowsEl.className = "list-rows";
		wrap.appendChild(rowsEl);

		const addBtn = document.createElement("button");
		addBtn.type = "button";
		addBtn.className = "btn secondary small list-row-add";
		addBtn.textContent = "+ Add";
		wrap.appendChild(addBtn);

		function sync() {
			const lines = Array.from(rowsEl.children)
				.map((row) => mode.format(row._getState()))
				.filter((line) => line !== "");
			textarea.value = lines.join("\n");
		}

		function addRow(values) {
			rowsEl.appendChild(buildRow(fields, mode, values || {}, sync));
		}

		textarea.value.split("\n").map((l) => l.trim()).filter(Boolean).forEach((line) => addRow(mode.parse(line)));

		addBtn.addEventListener("click", () => { addRow({}); sync(); });

		const form = textarea.closest("form");
		if (form) form.addEventListener("submit", sync);
	}

	document.addEventListener("DOMContentLoaded", () => {
		document.querySelectorAll("textarea[data-list-editor]").forEach(initEditor);
	});
})();
