## What this changes

<!-- One or two sentences. What behaviour is different after this is merged. -->

## Why

<!-- The problem it solves. Link an issue if one exists. -->

## How it was tested

<!--
Say what you actually ran, not what should work. If you tested on hardware, say which hardware:
board, radio or modem, and how it was connected. "Unit tests pass" and "tested on a Pi 5 with a
Heltec V4" are different claims and both are worth making.
-->

## Checklist

- [ ] `make fmt` has been run
- [ ] `make test` passes
- [ ] New handlers carry Swagger annotations
- [ ] New configuration is read with `os.Getenv` and has a sensible default, nothing hardcoded
- [ ] Database changes are a new migration, never an edit to an existing one
- [ ] Documentation updated if behaviour or configuration changed

<!--
For anything large, open an issue before writing the code so the shape can be agreed first. That is
not bureaucracy, it is to stop you spending an evening on something that conflicts with work already
in progress.
-->
