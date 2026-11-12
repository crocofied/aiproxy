import adapter from '@sveltejs/adapter-static';
import { sveltekit } from '@sveltejs/kit/vite';
import { defineConfig } from 'vite';

export default defineConfig({
	plugins: [
		sveltekit({
			// adapter-static turns the app into plain files. "fallback" makes
			// it a single-page app: the Go controller serves index.html for
			// every path and the router in the browser does the rest.
			adapter: adapter({ pages: 'build', assets: 'build', fallback: 'index.html' })
		})
	],
	server: {
		// "npm run dev": forward API calls to a locally running controller.
		proxy: { '/api': 'http://127.0.0.1:8080' }
	}
});
