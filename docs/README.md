# docs

`interactive.gif` (used in the top-level README) is recorded with
[VHS](https://github.com/charmbracelet/vhs) from `interactive.tape`, against a
throwaway Keycloak with a small demo realm, so it can be regenerated after
UI changes:

```sh
brew install vhs                     # also installs ttyd; needs ffmpeg
# start a disposable Keycloak on :8080 with admin/admin, then:
docs/demo-seed.sh
vhs docs/interactive.tape            # writes docs/interactive.gif
```

The seed script and the tape use `/tmp/kcdemo` for kcctl's config, session
and history, so your own contexts are untouched.
