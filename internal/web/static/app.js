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

// Update-available banner (dashboard.html): checked entirely client-side,
// fresh on every dashboard load - the browser asks GitHub for the latest
// release and compares it against the version baked into the page, so the
// admin server never talks to GitHub and keeps no update-related state.
// Dismissing hides it until a newer version ships, at which point the
// stored version no longer matches the fetched one and it reappears.
(function () {
	const banner = document.getElementById("update-banner");
	if (!banner) return;
	const current = banner.dataset.currentVersion;
	if (!current) return;

	// Mirrors internal/updatecheck's old Go version comparison: split off
	// any "-"/"+" suffix, compare dotted numeric segments left to right,
	// treat a missing segment as 0. Anything that doesn't parse this way is
	// "not newer" rather than wrongly flagging every build as outdated.
	function parseVersion(v) {
		v = v.trim().replace(/^v/, "").split(/[-+]/)[0];
		if (!v) return null;
		const parts = v.split(".").map(Number);
		return parts.every((n) => Number.isInteger(n)) ? parts : null;
	}
	function isNewer(latest, current) {
		const lp = parseVersion(latest);
		const cp = parseVersion(current);
		if (!lp || !cp) return false;
		for (let i = 0; i < Math.max(lp.length, cp.length); i++) {
			const l = lp[i] || 0, c = cp[i] || 0;
			if (l !== c) return l > c;
		}
		return false;
	}

	fetch("https://api.github.com/repos/emmtvv/swarmdash/releases/latest", {
		headers: { Accept: "application/vnd.github+json" },
	})
		.then((res) => (res.ok ? res.json() : Promise.reject(res.status)))
		.then((data) => {
			const latest = (data.tag_name || "").replace(/^v/, "");
			if (!latest || !isNewer(latest, current)) return;
			try {
				if (localStorage.getItem("sd-update-dismissed") === latest) return;
			} catch (e) {}

			banner.querySelector("[data-update-latest]").textContent = latest;
			const link = banner.querySelector("[data-update-link]");
			if (data.html_url) link.href = data.html_url;
			else link.remove();
			banner.hidden = false;

			const dismiss = document.getElementById("update-banner-dismiss");
			if (dismiss) {
				dismiss.addEventListener("click", () => {
					try { localStorage.setItem("sd-update-dismissed", latest); } catch (e) {}
					banner.remove();
				});
			}
		})
		.catch(() => {}); // GitHub unreachable/rate-limited: fail silently, no banner
})();

// Node picker: auto-submits its <form> on change (volumes/images pages'
// node <select>), replacing an inline onchange= that CSP's script-src
// blocks (nonces only cover <script> elements, not event-handler attrs).
(function () {
	document.querySelectorAll("select[data-auto-submit]").forEach((select) => {
		// requestSubmit (not submit) so the "submit" listener above still
		// adds the CSRF token when the form is a POST (the language picker).
		select.addEventListener("change", () => (select.form.requestSubmit ? select.form.requestSubmit() : select.form.submit()));
	});
})();

// Reset-password forms (users.html): prompts for a new password client-side
// via data-prompt-password="<username>" instead of an inline onsubmit=,
// then fills the hidden password field before the form submits.
(function () {
	document.querySelectorAll("form[data-prompt-password]").forEach((form) => {
		form.addEventListener("submit", (e) => {
			const p = prompt(t("New password for %s:", form.dataset.promptPassword));
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
// clicking a header cell sorts by that column (numeric-aware; a cell's
// data-sort attribute, when present, is sorted on instead of its text -
// e.g. raw bytes behind a "1.2 GiB" label).
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
			th.title = t("Click to sort");
			let asc = true;
			th.addEventListener("click", () => {
				const rows = Array.from(tbody.rows);
				rows.sort((a, b) => {
					const cellVal = (c) => (c ? (c.dataset.sort !== undefined ? c.dataset.sort : c.textContent.trim()) : "");
					const av = cellVal(a.cells[idx]);
					const bv = cellVal(b.cells[idx]);
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

// Command palette (Cmd/Ctrl+K): page jump targets come straight from the
// sidebar's own <a> elements (so they never drift out of sync with the
// nav), and anything typed is also sent to /search, which looks across
// services, stacks, nodes, tasks, networks, secrets and configs. Opening it
// with nothing typed shows the user's favorites first.
(function () {
	const overlay = document.getElementById("cmdk-overlay");
	if (!overlay) return;
	const input = document.getElementById("cmdk-input");
	const list = document.getElementById("cmdk-list");
	let items = [];
	let active = 0;
	let lastFocus = null;
	let remote = [];
	let searching = false;
	let seq = 0;
	let timer = null;

	const groupIcons = {
		favorite: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linejoin="round"><path d="M12 3.2 L14.7 8.7 L20.7 9.6 L16.35 13.85 L17.4 19.85 L12 17 L6.6 19.85 L7.65 13.85 L3.3 9.6 L9.3 8.7 Z"/></svg>',
		task: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><polyline points="3,13 8,13 10,7 14,19 16,13 21,13"/></svg>',
	};
	// Other result kinds borrow the icon of the sidebar link for their list
	// page, so a service hit looks like the Services nav entry.
	const kindPages = { service: "/services", stack: "/stacks", node: "/nodes", network: "/networks", secret: "/secrets", config: "/configs" };
	function iconFor(kind) {
		if (groupIcons[kind]) return groupIcons[kind];
		const a = document.querySelector('.sidebar .nav-scroll a[href="' + kindPages[kind] + '"] svg');
		return a ? a.outerHTML : "";
	}

	function pageGroups() {
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

	function addItem(iconHTML, label, detail, href) {
		const row = document.createElement("div");
		row.className = "cmdk-item";
		row.setAttribute("role", "option");
		row.innerHTML = iconHTML;
		const l = document.createElement("span");
		l.textContent = label;
		row.appendChild(l);
		if (detail) {
			const d = document.createElement("span");
			d.className = "cmdk-detail";
			d.textContent = detail;
			row.appendChild(d);
		}
		row.dataset.href = href;
		row.addEventListener("mouseenter", () => setActive(items.indexOf(row)));
		row.addEventListener("click", () => { window.location.href = href; });
		list.appendChild(row);
		items.push(row);
	}

	function addLabel(text) {
		const label = document.createElement("div");
		label.className = "cmdk-group-label";
		label.textContent = text;
		list.appendChild(label);
	}

	function render() {
		const q = input.value.trim().toLowerCase();
		list.innerHTML = "";
		items = [];
		remote.forEach((g) => {
			addLabel(g.label);
			g.hits.forEach((h) => addItem(iconFor(g.kind), h.label, h.detail, h.href));
		});
		pageGroups().forEach((group) => {
			const matches = group.links.filter((a) => a.textContent.toLowerCase().includes(q));
			if (!matches.length) return;
			addLabel(group.label);
			matches.forEach((a) => {
				addItem(a.querySelector("svg") ? a.querySelector("svg").outerHTML : "", a.textContent, "", a.getAttribute("href"));
			});
		});
		if (!items.length) {
			const empty = document.createElement("div");
			empty.className = "cmdk-empty";
			empty.textContent = searching ? t("Searching...") : (q ? t("No results.") : t("No matching pages."));
			list.appendChild(empty);
		}
		setActive(0);
	}

	function search() {
		const q = input.value.trim();
		const mine = ++seq;
		searching = true;
		fetch("/search?q=" + encodeURIComponent(q), { headers: { "Accept": "application/json" } })
			.then((r) => r.ok ? r.json() : { groups: [] })
			.then((data) => {
				if (mine !== seq) return; // a newer keystroke's search owns the list
				remote = data.groups || [];
				searching = false;
				render();
			})
			.catch(() => { if (mine === seq) { searching = false; render(); } });
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
		input.value = "";
		remote = [];
		render();
		search();
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

	input.addEventListener("input", () => {
		// Pages filter instantly; the cluster search waits for a pause in
		// typing so it isn't one request per keystroke.
		render();
		clearTimeout(timer);
		timer = setTimeout(search, 180);
	});
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
		} else if (e.key === "/" && !overlay.classList.contains("open") && !/^(INPUT|TEXTAREA|SELECT)$/.test(document.activeElement.tagName) && !document.activeElement.isContentEditable) {
			// "/" opens search too, the way it does on GitHub and friends.
			e.preventDefault();
			open();
		} else if (e.key === "Escape" && overlay.classList.contains("open")) {
			close();
		}
	});
})();

// Favorite stars (partial_favorite.html): toggled in place with fetch so
// starring something from a long table doesn't reload the page.
(function () {
	const csrf = document.querySelector('meta[name="csrf-token"]');
	document.addEventListener("click", (e) => {
		const btn = e.target.closest(".fav-star");
		if (!btn) return;
		e.preventDefault();
		e.stopPropagation();
		const body = new URLSearchParams({ kind: btn.dataset.favKind, name: btn.dataset.favName });
		fetch("/favorites", {
			method: "POST",
			headers: { "Accept": "application/json", "X-CSRF-Token": csrf ? csrf.content : "" },
			body: body,
		}).then((r) => r.ok ? r.json() : r.text().then((msg) => { throw new Error(msg); }))
			.then((data) => {
				btn.classList.toggle("on", data.favorite);
				btn.setAttribute("aria-pressed", data.favorite ? "true" : "false");
				const label = data.favorite ? t("Remove from favorites") : t("Add to favorites");
				btn.title = label;
				btn.setAttribute("aria-label", label);
			})
			.catch((err) => { alert(err.message); });
	});
})();

// Copy buttons: data-copy-text="..." copies that text (recovery codes).
(function () {
	document.addEventListener("click", (e) => {
		const btn = e.target.closest("[data-copy-text]");
		if (!btn || !navigator.clipboard) return;
		navigator.clipboard.writeText(btn.dataset.copyText.trim()).then(() => {
			const prev = btn.textContent;
			btn.textContent = t("Copied");
			setTimeout(() => { btn.textContent = prev; }, 1500);
		});
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
	const bytesHuman = sdBytes;

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
				{ key: "key", placeholder: t("KEY"), flex: "1" },
				{ key: "value", placeholder: t("value"), flex: "2" },
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
				{ key: "name", placeholder: t("name"), flex: "1" },
				{ key: "target", placeholder: t("target filename (optional)"), flex: "1" },
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
				{ key: "source", placeholder: t("/host/path or volume name"), flex: "2" },
				{ key: "target", placeholder: t("/container/path"), flex: "2" },
				{ key: "ro", type: "checkbox", label: t("Read-only") },
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
		removeBtn.setAttribute("aria-label", t("Remove"));
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
		addBtn.textContent = "+ " + t("Add");
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
