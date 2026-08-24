// Evidence merges this file into its generated SvelteKit config
// (see loadUserConfiguration in .evidence/template/svelte.config.js).
//
// Why it exists: Evidence resolves page queries in the browser via DuckDB
// WASM, so the server-rendered HTML of pages/index.md contains no links to
// /web/<project> or /product/<project>. SvelteKit's prerender crawler
// therefore never reaches those templated routes and fails the build with
// "marked as prerenderable, but were not prerendered". Listing the routes
// explicitly is the supported fix, so we read the project aliases straight
// out of the analytics database at build time.
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
	const sourceDir = path.join(root, 'sources', 'analytics');
	let filename = process.env.EVIDENCE_SOURCE__analytics__filename;
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

function prerenderEntries() {
	let aliases = [];
	try {
		aliases = projectAliases();
	} catch (err) {
		console.warn(`svelte.config.js: could not read project aliases (${err.message})`);
	}
	if (aliases.length === 0) aliases = [PLACEHOLDER];
	return ['*', ...aliases.flatMap((a) => [`/web/${a}`, `/product/${a}`])];
}

export default {
	kit: {
		prerender: {
			entries: prerenderEntries()
		}
	}
};
