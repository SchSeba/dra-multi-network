# DRA Multi-Network — Project Walkthrough

Open `index.html` directly in a desktop browser. The 26-slide deck works offline,
without a server, fonts, libraries or image downloads. Allow 15–20 minutes plus Q&A.

- **Arrow keys / Space / Page Up / Page Down:** navigate.
- **Home / End:** first / last slide.
- **F:** fullscreen; **N:** speaker notes; **O:** slide overview.
- **Escape:** close notes or overview. Swipe horizontally on touch screens.
- **Print / PDF:** export all slides, one landscape slide per page. Use zero
  margins and disable browser headers/footers; enable background graphics.
- Slide numbers are saved in the URL (`#12` opens slide 12).

`dra-multi-network.pdf` is an exported backup of all 26 slides.

Read `speaker-notes.md` for rehearsal without displaying notes to the audience.
The on-screen notes overlay is for preparation; it will be visible if you share
that browser window. Source links work when the deck stays in this checkout.
You can copy `index.html` alone for presentation; optional source links then
require the repository.

The deck distinguishes current kubelet gRPC from in-process planners/native
execution and a **revised proposed target** where external gRPC network plugins
perform both planning and native mutations. This revised target is not implemented
and is documented in `docs/grpc-plugins.md`, replacing the earlier planner-only
proposal. Vendors own supported native actions without core handler changes.
The stateful IPAM provider remains a separate future gRPC boundary. Validation slides summarize the
repository's recorded lab results, not a new acceptance run. No physical NIC,
fabric or complete host-reboot recovery claim is made.

Optional browser check (uses a temporary Playwright install):

```sh
npm install --prefix /tmp/dra-deck-check playwright
/tmp/dra-deck-check/node_modules/.bin/playwright install chromium
NODE_PATH=/tmp/dra-deck-check/node_modules node docs/presentation/check.cjs
```

On a minimal Linux image, Playwright may also need `playwright install-deps chromium`.
