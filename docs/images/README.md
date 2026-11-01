# README capture provenance

All four PNGs are real renders of Dibs source at
`ea43f76b15f9aaa64cfa8ba0a8eae530ad3c16f8`, using synthetic fixture data.
They contain no live fleet, real agent names, hostnames or user paths.

- `board-light.png` and `board-dark.png`: the unchanged `resources/read`
  HTML from a clean release build, through the real pinned MCP Apps
  `AppBridge` and `PostMessageTransport`, in a browser iframe. A development
  build supplies only the compile-time OS-presence fixture; its app HTML was
  compared byte for byte with that release resource (SHA-256
  `a61264c6c35dd51cc73d3bcfbec00ff1985f50d7010d4cf9e6f6408a4376a85d`).
  Both binaries report the source revision above and `vcs.modified=false`.
  The surrounding chat is an unbranded fixture host, not a screenshot of a
  ChatGPT or Claude conversation. Its 60 agents have role names: eight are
  active, including the pinned viewer, and 52 are dormant. The human is
  confirmed, no row composer is open, and one real fixture question appears
  above Active. Coordination ages range from now through minutes and hours;
  host metadata says `web-app 1`. Search was exercised and cleared, fonts
  loaded and finite row animations finished before capture. Both appearances
  reported no browser errors. The browser raster scale is 2x (1376 by 1394).
- `native-prompt-light.png` and `native-prompt-dark.png`: the shipped Swift
  source catalog compiled with the existing broker/reading fixture switches,
  using real AppKit controls, bundled fonts and canonical mark. The fixture
  posts synthetic mail, opens its reading view and types a reply. Content-view
  rendering uses a 1280 by 800 screen fixture at 120% text and 2x raster scale
  (1344 by 760). The full message, reply and explicit actions are visible.
  No live desktop window, notification permission or user application is
  contacted.

These images illustrate the product with fixtures. They are not installation,
desktop notification visibility or fleet-delivery acceptance evidence.
