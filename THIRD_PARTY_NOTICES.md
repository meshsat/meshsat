# Third-Party Notices — MeshSat Bridge

MeshSat itself is GPL-3.0 (see `LICENSE`). Go module dependencies and their
licences are declared in `go.mod`/`go.sum`. In addition, the published Docker
image builds and bundles the following third-party C programs:

| Component | Source | Licence | Modifications |
|---|---|---|---|
| Direwolf (branch 1.8) | github.com/wb2osz/direwolf | GPL-2.0-or-later | KISS TCP server bind changed from `INADDR_ANY` to loopback. Applied as a one-line in-tree edit at image build time; the exact edit is recorded in `Dockerfile` (see the `[MESHSAT-517]` step). |
| librtlsdr (RTL-SDR Blog fork) | github.com/rtlsdrblog/rtl-sdr-blog | GPL-2.0 | `rtl_power` patched to call `rtlsdr_reset_buffer` before each sync read — `docker-patches/rtl_power-v4-reset-buffer.patch`. |
| rtl_power_fftw | github.com/AD-Vega/rtl-power-fftw | GPL-3.0 | none |

## Corresponding source

Every modification this project applies to the components above is published
in this repository (`Dockerfile`, `docker-patches/`), and the upstream sources
are fetched at pinned branches from the public repositories listed. Together
these constitute the complete corresponding source for the GPL components in
any published image. If you need an archived copy of the corresponding source
for a specific image, contact the maintainers (see `AUTHORS`) and we will
provide it.
