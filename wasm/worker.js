// The Web Worker that runs the Go program: the model takes a few seconds
// to load and a document a few seconds to process, which would freeze
// the page in its main thread. The model files are kept in the browser's
// Cache Storage, downloaded once per version (model/manifest.json).
// The page names the build in the worker's URL (worker.js?v=…, see make
// wasm): the files fetched here carry it too, so that the browser's HTTP
// cache never mixes a script of one build with the module of another.
const build = self.location.search;
importScripts("memfs.js" + build, "wasm_exec.js" + build);

// The smallest module with a 128-bit SIMD instruction: browsers accept it
// when their engine, and the processor under it, support WebAssembly SIMD.
const simd128 = WebAssembly.validate(new Uint8Array([
  0, 97, 115, 109, 1, 0, 0, 0, 1, 5, 1, 96, 0, 1, 123, 3, 2, 1, 0, 10, 10, 1, 8, 0, 65, 0, 253, 15, 253, 98, 11,
]));

let goReady, goFailed;
const ready = new Promise((resolve, reject) => { goReady = resolve; goFailed = reject; });
self.onGoReady = (info) => goReady(info);
self.onGoError = (message) => goFailed(new Error(message));

const post = (msg, transfer) => self.postMessage(msg, transfer || []);

async function start() {
  const t0 = performance.now();
  const manifest = await (await fetch("model/manifest.json", { cache: "no-cache" })).json();
  const name = "indecis-caviardage-" + manifest.version;
  let cache = null;
  if (self.caches) {
    // Cache Storage needs a secure context (https, or localhost).
    cache = await caches.open(name);
    for (const k of await caches.keys()) if (k.startsWith("indecis-caviardage-") && k !== name) await caches.delete(k);
  }
  const total = manifest.files.reduce((s, f) => s + f.size, 0);
  let loaded = 0, fromCache = true, stored = !!cache;
  for (const f of manifest.files) {
    const url = "model/" + f.name;
    let r = cache && await cache.match(url), putting = null;
    if (!r) {
      fromCache = false;
      r = await fetch(url);
      if (!r.ok) throw new Error(url + " : " + r.status);
      // Stored while it downloads, awaited after: awaited first, it
      // would hide the progress. The browser may refuse it (storage
      // quota): the model then downloads at each visit.
      if (cache) putting = cache.put(url, r.clone()).catch(() => { stored = false; });
    }
    const reader = r.body.getReader();
    const data = new Uint8Array(f.size);
    let at = 0, shown = 0;
    for (;;) {
      const { done, value } = await reader.read();
      if (done) break;
      data.set(value, at);
      at += value.length;
      loaded += value.length;
      if (loaded - shown > total / 200 || loaded === total) {
        shown = loaded;
        post({ type: "progress", loaded, total, fromCache });
      }
    }
    await putting;
    fs.addFile("/model/" + f.name, data);
  }

  const go = new Go();
  go.argv = ["indecis-caviardage"];
  go.env = { TMPDIR: "/tmp" };
  const wasm = simd128 ? "caviardage.wasm" : "caviardage-nosimd.wasm";
  const { instance } = await WebAssembly.instantiateStreaming(fetch(wasm + build), go.importObject);
  go.run(instance).then(() => goFailed(new Error("le programme Go s'est arrêté")));
  const info = await ready;
  post({
    type: "ready",
    seconds: (performance.now() - t0) / 1000,
    fromCache,
    stored,
    hardware: { simd128, wasm, cores: navigator.hardwareConcurrency || 0, ...info },
  });
}

self.onmessage = async ({ data: { id, name, bytes, bias, strategy, keep, release } }) => {
  try {
    const { response, document } = await self.caviarder(name, new Uint8Array(bytes), String(bias), strategy, keep, release,
      (done, total) => post({ type: "work", id, done, total }));
    post({ type: "result", id, response: JSON.parse(response), document: document.buffer }, [document.buffer]);
  } catch (e) {
    post({ type: "result", id, error: e.message });
  }
};

start().catch((e) => post({ type: "error", message: e.message }));
