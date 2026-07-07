// Clusterra Node-RED settings. Adapted from the upstream default. Only the
// pieces that diverge from the stock image are commented — everything else
// is upstream defaults.
module.exports = {
    // Bind on all interfaces — the reverse-proxy (cluster-api) lives in a
    // different VPC. Cookie auth at the proxy gates access; CIDR locking is
    // not useful given that.
    uiHost: '0.0.0.0',
    uiPort: process.env.PORT || 1880,

    // The reverse-proxy mounts every session at
    // /nodered/<cluster_id>/<job_id>/. Node-RED needs to know its prefix so
    // editor assets resolve and the WebSocket upgrade lands on the right path.
    httpAdminRoot: process.env.BASE_URL || '/',
    httpNodeRoot: (process.env.BASE_URL || '') + '/api',

    userDir: process.env.NODE_RED_USER_DIR || '/data',
    flowFile: 'flows.json',

    // Disable Node-RED's own flow-credentials encryption. We don't store
    // secrets in flow nodes — the per-session bearer lives in
    // clusterra-creds.json which is owned by the sbatch script and gated by
    // userDir filesystem perms. Setting `false` silences the startup
    // "credentialSecret unset" warning without introducing a stable key
    // that would have to be managed.
    credentialSecret: false,

    // Editor auth disabled — cookie auth at the reverse-proxy is the gate.
    // Adding adminAuth here would force users to type a second password.
    // adminAuth: undefined,

    logging: {
        console: { level: 'info', metrics: false, audit: false },
    },

    editorTheme: {
        page: { title: 'Clusterra · Node-RED' },
        header: { title: 'Clusterra' },
        palette: { editable: true },
        projects: { enabled: false },
        // Ship our examples folder under Examples → Clusterra.
        examples: { directory: '/data/examples' },
    },

    // Function-node sandbox can't `require()` by default. Expose `fs` so flows
    // that read `clusterra-creds.json` can do `global.get('fs')` without
    // toggling functionExternalModules per-node. Add other commonly-needed
    // modules here as flows grow.
    functionGlobalContext: {
        fs: require('fs'),
        path: require('path'),
        // http/https/url are used by the bundled clusterra-api SSE branch and
        // the clusterra-init bootstrap renderer (no streaming mode in the
        // built-in http-request node, so we drive the request directly).
        http: require('http'),
        https: require('https'),
        url: require('url'),
        // Node-RED 4 function sandbox doesn't expose `process` — pre-resolve
        // the userDir at settings load so flows can `global.get('userDir')`
        // without each one having to pull it off the env.
        userDir: process.env.NODE_RED_USER_DIR || '/data',
        // Pre-resolved admin-API coordinates so clusterra-init can POST
        // rendered subflows to its own runtime without the function-node
        // sandbox having to read process.env.
        port: parseInt(process.env.PORT || '1880', 10),
        baseUrl: process.env.BASE_URL || '',
    },

    // Allow function nodes to declare additional npm modules via the editor's
    // Setup → Modules tab. Off by default in older Node-RED, useful for
    // customer-authored flows that need axios / lodash / etc.
    functionExternalModules: true,
};
