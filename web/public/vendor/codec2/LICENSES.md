# Codec2 decoder, vendored (MESHSAT-1735)

Two files, loaded on demand by the SPA when a Codec2 voice note is played; never part of the main
bundle. Keep them beside each other: the glue finds the wasm with `new URL("codec2.wasm",
import.meta.url)`.

| File | What | Licence |
|---|---|---|
| `codec2.wasm` | libcodec2 (David Rowe and contributors), built from codec2 commit `310777b` (github.com/drowe67/codec2, 16 Mar 2026) with emsdk 5.0.7, every mode compiled in | LGPL-2.1 (codec2's `COPYING`) |
| `codec2.js` | the emscripten glue for that build (ES module, `MODULARIZE`, exports `createCodec2`) | MIT (the wrapper project's licence) |

Source of both: https://github.com/belteshazzar/codec2-web (MIT). The build is not checked in there;
its GitHub Actions workflow builds the module with `build.sh`
(https://github.com/belteshazzar/codec2-web/blob/main/build.sh) and publishes it to
https://belteshazzar.github.io/codec2-web/vendor/codec2.wasm and `vendor/codec2.js`, where these
copies were taken from on 28 Oct 2026.

Pinned:

```
sha256  2a459fc9a2b1be6ecfe174774f679623d409dabf6a08d412e1e39a2e0a9cdf92  codec2.wasm  (193,328 B)
sha256  65b6317d3996652e75a7b5a791ca08f141759b64fd2988f1905b652aa5c041e0  codec2.js    (9,882 B)
```

A rebuild changes the export letters inside the glue as well as the wasm: replace both files
together, verify the new sums and update this table. LGPL-2.1 compliance: the library ships as its
own file, unmodified, with this notice and a link to its source; the SPA calls it through the
public `codec2_*` functions only (`web/src/utils/codec2.js`). `web/tests/codec2/roundtrip.test.js`
proves the copy encodes and decodes.
