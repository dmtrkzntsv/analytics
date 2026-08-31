<script>
	// Shadows Evidence's default layout (.evidence/template/src/pages/+layout.svelte)
	// to swap the Evidence wordmark in the header for ours. The relative app.css
	// import resolves after the CLI copies this file into the template.
	import '@evidence-dev/tailwind/fonts.css';
	import '../app.css';
	import { EvidenceDefaultLayout } from '@evidence-dev/core-components';
	export let data;
</script>

<EvidenceDefaultLayout {data} lightLogo="/logo-light.svg" darkLogo="/logo-dark.svg">
	<slot slot="content" />
</EvidenceDefaultLayout>
