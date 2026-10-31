# Native prompt review: 378262

Synthetic mail only. These images are review evidence, not a production install
or a claim that a notification was visible to its recipient.

| Appearance | Before: hidden picker and abbreviated text | After: full text, visible choices and reply |
|---|---|---|
| Light, 100% | ![Old light picker](before-light.png) | ![Light native prompt](light-100.png) |
| Dark, 100% | ![Old dark picker](before-dark.png) | ![Dark native prompt](dark-100.png) |

| Large text | Light | Dark |
|---|---|---|
| 150% | ![Light 150%](light-150.png) | ![Dark 150%](dark-150.png) |
| 160%, 1280×800 screen | ![Light 160%](light-160.png) | ![Dark 160%](dark-160.png) |

At 160%, the message, choices and reply share the scroll region. The header and
footer remain pinned. “More below” signals content beyond the viewport. Tab
reveals the choices; editing reveals the reply field:

![Dark 160% after typing](dark-160-reply.png)

## How these were measured

The before images use the notifier source at `f16b91573657b842431332bfbeea61634ef4920b`
with a capture fixture and activation disabled. ScreenCaptureKit captured only
that private fixture's own window; no other app or real mail was captured. The
inactive light alert's window chrome can differ from an active modal alert.

The after images are AppKit content-view renders from
`TestNativePromptFullTextChoicesTypingAndNoImplicitApproval`. The test compiles
the shipped Swift source with a compile-time notification/user-gesture fixture;
it opens no live window, requests no authorisation and contacts no ChatGPT app.
It checks full raw and rendered message text, last-glyph visibility and bottom
space, wrapped choice labels, kind-label width, accessibility roles, native Tab
navigation, typed and choice-plus-note replies, and explicit request decisions.
The bundled fonts and canonical mark are used. All text sizes are captured
against a 1280×800 screen fixture, at the test machine's backing resolution.

The before presenter fails the new real-entry checks: long text is shortened,
the choices/note are lost, request comments become an approval path, and old
helper receipts do not report their missing reply field. Separate exact-old-file
controls fail for the new human-comment API and JSON-escaped 32 KiB answer.
Those controls are not a production desktop or fleet test.

The extra render check samples the pinned mark after scrolling. It passes with
the explicit viewport clipping. Removing that clipping did not reproduce the
observed overpaint in two bounded controls, so those controls are **not** claimed
as a demonstrated regression discriminator. The earlier before-code failures
above are independently observed.

Regenerate after images with:

```console
DIBS_PROMPT_SCREENSHOTS=/tmp/dibs-prompt-review go test ./internal/notify -run '^TestNativePromptFullTextChoicesTypingAndNoImplicitApproval$' -count=1
```

The diagnostic probe measures the installed notifier image. A running broker
mapped from an older image is separately qualified by its pipe capability and
posting receipt; an installed-image probe alone does not prove it refreshed.
