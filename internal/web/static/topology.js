// Dependency-free force-directed graph for the cluster topology map.
// Nodes are services and overlay networks; an edge means "this service is
// attached to this network" - two services read as connected whenever a
// line runs between them via a shared network node.
function renderTopology(svg, graph, opts) {
	opts = opts || {};
	if (!graph.nodes.length) return;

	const SVGNS = "http://www.w3.org/2000/svg";
	const seriesColors = getCategoricalColors(8);
	const stackColor = assignStackColors(graph.nodes, seriesColors);

	const nodes = graph.nodes.map((n) => ({
		...n,
		r: n.type === "network" ? 15 : 20,
		x: svg.clientWidth / 2 + (Math.random() - 0.5) * 400,
		y: svg.clientHeight / 2 + (Math.random() - 0.5) * 400,
		vx: 0,
		vy: 0,
	}));
	const byID = new Map(nodes.map((n) => [n.id, n]));
	const edges = graph.edges
		.map((e) => ({ source: byID.get(e.source), target: byID.get(e.target) }))
		.filter((e) => e.source && e.target);

	let alpha = 1;
	const alphaDecay = 0.985;
	const alphaMin = 0.005;
	const linkDistance = 140;
	const chargeStrength = 4200;

	function tick() {
		for (let i = 0; i < nodes.length; i++) {
			for (let j = i + 1; j < nodes.length; j++) {
				const a = nodes[i], b = nodes[j];
				let dx = a.x - b.x, dy = a.y - b.y;
				let dist2 = dx * dx + dy * dy;
				if (dist2 < 1) { dx = (Math.random() - 0.5); dy = (Math.random() - 0.5); dist2 = 1; }
				const dist = Math.sqrt(dist2);
				const force = (chargeStrength * alpha) / dist2;
				const fx = (dx / dist) * force, fy = (dy / dist) * force;
				a.vx += fx; a.vy += fy;
				b.vx -= fx; b.vy -= fy;
			}
		}
		for (const e of edges) {
			let dx = e.target.x - e.source.x, dy = e.target.y - e.source.y;
			const dist = Math.sqrt(dx * dx + dy * dy) || 1;
			const force = (dist - linkDistance) * 0.06 * alpha;
			const fx = (dx / dist) * force, fy = (dy / dist) * force;
			e.source.vx += fx; e.source.vy += fy;
			e.target.vx -= fx; e.target.vy -= fy;
		}
		const cx = svg.clientWidth / 2, cy = svg.clientHeight / 2;
		for (const n of nodes) {
			n.vx += (cx - n.x) * 0.006 * alpha;
			n.vy += (cy - n.y) * 0.006 * alpha;
			if (n.fx != null) {
				n.x = n.fx; n.y = n.fy; n.vx = 0; n.vy = 0;
				continue;
			}
			n.vx *= 0.82; n.vy *= 0.82;
			n.x += n.vx; n.y += n.vy;
		}
		alpha *= alphaDecay;
	}

	for (let i = 0; i < 150 && alpha > alphaMin; i++) tick();

	svg.innerHTML = "";
	const viewport = document.createElementNS(SVGNS, "g");
	viewport.setAttribute("class", "topo-viewport");
	svg.appendChild(viewport);
	const edgeLayer = document.createElementNS(SVGNS, "g");
	const nodeLayer = document.createElementNS(SVGNS, "g");
	viewport.appendChild(edgeLayer);
	viewport.appendChild(nodeLayer);

	const edgeEls = edges.map((e) => {
		const line = document.createElementNS(SVGNS, "line");
		line.setAttribute("class", "topo-edge");
		edgeLayer.appendChild(line);
		return { el: line, edge: e };
	});

	const nodeEls = nodes.map((n) => {
		const g = document.createElementNS(SVGNS, "g");
		g.setAttribute("class", "topo-node topo-node-" + n.type);
		g.dataset.id = n.id;

		let shape;
		if (n.type === "network") {
			shape = document.createElementNS(SVGNS, "polygon");
			const r = n.r;
			shape.setAttribute("points", `0,${-r} ${r},0 0,${r} ${-r},0`);
			shape.setAttribute("class", "topo-shape" + (n.ingress ? " topo-ingress" : ""));
		} else {
			shape = document.createElementNS(SVGNS, "circle");
			shape.setAttribute("r", n.r);
			shape.setAttribute("class", "topo-shape topo-status-" + serviceStatus(n));
			shape.style.fill = n.stack ? stackColor.get(n.stack) : "var(--topo-neutral)";
		}
		g.appendChild(shape);

		const label = document.createElementNS(SVGNS, "text");
		label.setAttribute("class", "topo-label");
		label.setAttribute("y", n.r + 14);
		label.textContent = n.label;
		g.appendChild(label);

		if (n.type === "service") {
			const sub = document.createElementNS(SVGNS, "text");
			sub.setAttribute("class", "topo-sublabel");
			sub.setAttribute("y", n.r + 27);
			sub.textContent = `${n.running}/${n.desired}`;
			g.appendChild(sub);
		}

		nodeLayer.appendChild(g);
		return { el: g, node: n };
	});

	function positionAll() {
		for (const { el, node } of nodeEls) el.setAttribute("transform", `translate(${node.x},${node.y})`);
		for (const { el, edge } of edgeEls) {
			el.setAttribute("x1", edge.source.x);
			el.setAttribute("y1", edge.source.y);
			el.setAttribute("x2", edge.target.x);
			el.setAttribute("y2", edge.target.y);
		}
	}
	positionAll();

	// --- pan / zoom ---
	let panX = 0, panY = 0, scale = 1;
	function applyTransform() {
		viewport.setAttribute("transform", `translate(${panX},${panY}) scale(${scale})`);
	}
	svg.addEventListener("wheel", (e) => {
		e.preventDefault();
		const rect = svg.getBoundingClientRect();
		const mx = e.clientX - rect.left, my = e.clientY - rect.top;
		const gx = (mx - panX) / scale, gy = (my - panY) / scale;
		scale = Math.min(2.5, Math.max(0.3, scale * (e.deltaY < 0 ? 1.1 : 0.9)));
		panX = mx - gx * scale;
		panY = my - gy * scale;
		applyTransform();
	}, { passive: false });

	let panning = null;
	svg.addEventListener("pointerdown", (e) => {
		if (e.target.closest(".topo-node")) return;
		panning = { x: e.clientX, y: e.clientY, panX, panY };
		svg.classList.add("panning");
	});
	window.addEventListener("pointermove", (e) => {
		if (!panning) return;
		panX = panning.panX + (e.clientX - panning.x);
		panY = panning.panY + (e.clientY - panning.y);
		applyTransform();
	});
	window.addEventListener("pointerup", () => { panning = null; svg.classList.remove("panning"); });

	function reset() {
		panX = 0; panY = 0; scale = 1;
		applyTransform();
	}
	if (opts.onResetRef) opts.onResetRef(reset);

	// --- node drag ---
	function toGraphPoint(clientX, clientY) {
		const rect = svg.getBoundingClientRect();
		return {
			x: (clientX - rect.left - panX) / scale,
			y: (clientY - rect.top - panY) / scale,
		};
	}
	let raf = null;
	function ensureLoop() {
		if (raf != null) return;
		function loop() {
			if (alpha > alphaMin) {
				tick();
				positionAll();
				raf = requestAnimationFrame(loop);
			} else {
				raf = null;
			}
		}
		raf = requestAnimationFrame(loop);
	}

	for (const { el, node } of nodeEls) {
		el.addEventListener("pointerdown", (e) => {
			e.stopPropagation();
			el.setPointerCapture(e.pointerId);
			const move = (ev) => {
				const p = toGraphPoint(ev.clientX, ev.clientY);
				node.fx = p.x; node.fy = p.y;
				alpha = Math.max(alpha, 0.3);
				ensureLoop();
			};
			const up = () => {
				node.fx = null; node.fy = null;
				el.removeEventListener("pointermove", move);
				el.removeEventListener("pointerup", up);
			};
			el.addEventListener("pointermove", move);
			el.addEventListener("pointerup", up);
		});

		el.addEventListener("click", () => selectNode(node));
	}

	// --- selection / highlight ---
	function neighborsOf(node) {
		const ids = new Set([node.id]);
		for (const e of edges) {
			if (e.source === node) ids.add(e.target.id);
			if (e.target === node) ids.add(e.source.id);
		}
		return ids;
	}
	let selected = null;
	function selectNode(node) {
		selected = selected === node ? null : node;
		applyHighlight();
		if (opts.onSelect) opts.onSelect(selected);
	}
	function applyHighlight(searchIDs) {
		const active = selected ? neighborsOf(selected) : null;
		for (const { el, node } of nodeEls) {
			const inSearch = !searchIDs || searchIDs.has(node.id);
			const inSelection = !active || active.has(node.id);
			el.classList.toggle("topo-dim", !inSearch || !inSelection);
		}
		for (const { el, edge } of edgeEls) {
			const inSearch = !searchIDs || (searchIDs.has(edge.source.id) && searchIDs.has(edge.target.id));
			const inSelection = !active || (active.has(edge.source.id) && active.has(edge.target.id));
			el.classList.toggle("topo-dim", !inSearch || !inSelection);
		}
	}
	svg.addEventListener("click", (e) => {
		if (!e.target.closest(".topo-node") && selected) {
			selected = null;
			applyHighlight();
			if (opts.onSelect) opts.onSelect(null);
		}
	});

	if (opts.onSearchRef) {
		opts.onSearchRef((query) => {
			if (!query) { applyHighlight(); return; }
			const q = query.toLowerCase();
			const matches = new Set(nodes.filter((n) => n.label.toLowerCase().includes(q)).map((n) => n.id));
			applyHighlight(matches);
		});
	}
}

function serviceStatus(n) {
	if (n.desired === 0) return "idle";
	if (n.running >= n.desired) return "ok";
	if (n.running === 0) return "crit";
	return "warn";
}

function assignStackColors(nodes, palette) {
	const stacks = [];
	for (const n of nodes) {
		if (n.type === "service" && n.stack && !stacks.includes(n.stack)) stacks.push(n.stack);
	}
	const map = new Map();
	stacks.forEach((s, i) => map.set(s, i < palette.length ? palette[i] : "var(--topo-neutral)"));
	return map;
}

function getCategoricalColors(n) {
	const out = [];
	for (let i = 1; i <= n; i++) out.push(`var(--topo-series-${i})`);
	return out;
}
