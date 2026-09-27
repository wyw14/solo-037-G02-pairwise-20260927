# WaveLab

WaveLab is a local HTTP service for tow-tank experiments. The original equipment and interlock pages expose a fixed commissioning diagnostic scenario. Operator measurements are a separate live data path.

Set `WAVELAB_DATA_DIR` to choose the archive directory (default `data/wavelab`). `POST /api/measured-runs` accepts `name`, `model_name`, `section`, and `carriage`. `POST /api/measured-runs/{id}/evidence` accepts `kind` (`wave_time` or `force`), `source_id`, finite `value`, and `recorded_at` in RFC3339 form. `GET /api/measured-runs`, `GET /api/measured-runs/{id}`, and `GET /api/measured-runs/{id}/analysis` return the supplied records and wave/force summary. Measurements are committed to `operator-measurements.json` before they are published in memory, and are reloaded when the service restarts. The archive does not automatically grant a safety permit or drive hardware.

The `internal/probe`, `internal/force`, `internal/permit`, `internal/carriage`, `internal/track`, `internal/wave`, `internal/absorber`, and `internal/journal` packages provide component logic for further work. Their diagnostic use does not imply that the original HTTP pages accept real sensor streams.

## Wave scheme library and execution plans

Operators can persist reusable wave-making schemes and execute them as plans against a specific experiment and tank section (UI at `/plans`).

- `GET/POST /api/wave-schemes`, `GET/PUT/DELETE /api/wave-schemes/{id}` manage schemes. A scheme records `name`, `frequency_hz` (0–10 Hz), `amplitude`, `duration_ms` (100 ms–1 h) and `sections` (the applicable tank sections; empty means every section). Schemes are committed atomically to `wave-schemes.json` and reloaded on restart.
- `POST /api/wave-plans/preflight` with `{run_id, scheme_id, section}` returns per-item checks (scheme exists and is valid, experiment exists and is not terminal, section exists, scheme applies to it, section unoccupied, wavemaker idle) without driving hardware.
- `POST /api/wave-plans` reruns the same checks atomically; any conflict is a hard `409 Conflict` (the rejected attempt is still recorded). On success the section is claimed in the tank registry and the wave controller issues commands every 20 ms for the scheme duration, recording every issued `DeviceCommand`.
- `GET /api/wave-plans`, `GET /api/wave-plans/{id}` (includes a `diff`), and `GET /api/wave-plans/{id}/diff` review executions. The diff reports planned vs actual command counts, setpoint mismatches, missing/extra commands (an abort truncates the timeline), and timing jitter. `POST /api/wave-plans/{id}/abort` stops a running plan through the same controller stop path as the emergency brake.
- Executions are persisted to `wave-plan-executions.json` (writes throttled while running, flushed on completion). A plan still `running` when the service restarts is marked `interrupted` on reload, since occupancy lives in the in-memory tank registry.

