// Evidence merges this file into its generated SvelteKit config
// (see loadUserConfiguration in .evidence/template/svelte.config.js).
//
// Why it exists: SvelteKit prerenders a templated route only if something
// names it, and two of ours are unreachable by the crawler. pages/index.md
// links /web/<project> and its five siblings from server-rendered HTML, but
// only once a project exists — a stack that has never ingested anything
// leaves the crawler nothing to follow. /web/<project>/page is worse: the
// only link to it is a table cell, and Evidence resolves the query behind it
// in the browser via DuckDB WASM, so it is absent from the built HTML with
// data and without. Either way the build fails with "marked as prerenderable,
// but were not prerendered". Listing the routes explicitly is the supported
// fix, so we read the project aliases straight out of the twillingate
// database at build time.
import fs from 'node:fs';
import path from 'node:path';
import { DatabaseSync } from 'node:sqlite';

// Prerendering needs at least one entry per templated route. When no project
// exists yet (a stack that has never ingested anything) this stand-in keeps
// the build green; the index links only real projects, so it stays unreachable.
const PLACEHOLDER = '__no_projects__';

function projectRoot() {
	// Evidence invokes this from inside .evidence/template.
	const cwd = process.cwd();
	return cwd.includes('.evidence') ? cwd.split('.evidence')[0] : cwd;
}

function databaseFile() {
	const root = projectRoot();
	const sourceDir = path.join(root, 'sources', 'twillingate');
	let filename = process.env.EVIDENCE_SOURCE__twillingate__filename;
	if (!filename) {
		// Minimal read of the one key we need out of connection.yaml.
		const yaml = fs.readFileSync(path.join(sourceDir, 'connection.yaml'), 'utf8');
		const match = yaml.match(/^\s*filename:\s*(\S+)/m);
		if (!match) throw new Error('svelte.config.js: no filename in connection.yaml');
		filename = match[1];
	}
	// The sqlite plugin resolves filename relative to the source directory.
	return path.resolve(sourceDir, filename);
}

function projectAliases() {
	const file = databaseFile();
	if (!fs.existsSync(file)) {
		console.warn(`svelte.config.js: ${file} not found; prerendering no project pages`);
		return [];
	}
	const db = new DatabaseSync(file, { readOnly: true });
	try {
		return db
			.prepare('select alias from projects order by alias')
			.all()
			.map((row) => row.alias);
	} finally {
		db.close();
	}
}

// Every page under pages/, as the route SvelteKit will serve it at.
function pageRoutes(dir, prefix = '') {
	const routes = [];
	for (const entry of fs.readdirSync(dir, { withFileTypes: true })) {
		if (entry.isDirectory()) {
			routes.push(...pageRoutes(path.join(dir, entry.name), `${prefix}/${entry.name}`));
		} else if (entry.name.endsWith('.md')) {
			const base = entry.name.slice(0, -'.md'.length);
			routes.push(base === 'index' ? prefix || '/' : `${prefix}/${base}`);
		}
	}
	return routes;
}

// The templated routes are read off the pages directory rather than listed
// here. A hand-kept copy of the tree goes stale silently: /web/[project]/page
// was missing from one, and since nothing links to it in server-rendered HTML
// either, every dashboards build failed on it — data or no data.
function templatedRoutes() {
	const routes = pageRoutes(path.join(projectRoot(), 'pages')).filter((r) => r.includes('['));
	for (const route of routes) {
		for (const param of route.match(/\[[^\]]*\]/g)) {
			if (param !== '[project]') {
				throw new Error(
					`svelte.config.js: ${route} takes ${param}; teach this file how to fill it`
				);
			}
		}
	}
	return routes;
}

function prerenderEntries() {
	let aliases = [];
	try {
		aliases = projectAliases();
	} catch (err) {
		console.warn(`svelte.config.js: could not read project aliases (${err.message})`);
	}
	if (aliases.length === 0) aliases = [PLACEHOLDER];
	const templated = templatedRoutes();
	// '*' covers every route that takes no parameter. The templated ones need
	// at least one entry each, or the build fails with "marked as
	// prerenderable, but were not prerendered".
	return [
		'*',
		...aliases.flatMap((a) => templated.map((r) => r.replaceAll('[project]', a)))
	];
}

export default {
	kit: {
		prerender: {
			entries: prerenderEntries()
		}
	}
};
