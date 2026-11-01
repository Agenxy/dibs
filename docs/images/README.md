# README capture provenance

All four PNGs are real renders of Dibs source at
`64c80f2dbd38c849bc491be8557fa7e0cfee6948`, using synthetic fixture data.
They contain no live fleet, real agent names, hostnames or user paths.

- `board-light.png` and `board-dark.png`: the unchanged `resources/read`
  HTML served by a clean main64 daemon, through the real pinned MCP Apps
  `AppBridge` and `PostMessageTransport`, in a browser iframe. The surrounding
  chat is an unbranded fixture host, not a screenshot of a ChatGPT or Claude
  conversation. Its 60 agents have role names. The captures show the pinned
  viewer, recency order, eight recent rows, search and collapsed Dormant group.
  Search was exercised and cleared, fonts loaded and finite row animations
  finished before capture. Both appearances reported no browser errors.
- `native-prompt-light.png` and `native-prompt-dark.png`: the shipped Swift
  source catalog compiled with the existing broker/reading fixture switches,
  using real AppKit controls, bundled fonts and canonical mark. The fixture
  posts synthetic mail, opens its reading view and types a reply. Content-view
  rendering uses a 1280×800 screen fixture at 120% text and 1× raster scale.
  The full message, reply and explicit actions are visible. No live desktop
  window, notification permission or user application is contacted.

These images illustrate the product with fixtures. They are not installation,
desktop notification visibility or fleet-delivery acceptance evidence.
