<!--
Thanks for sending this. Nothing below is a hoop: each line is something that
has actually caught a bug in this repository, and the last two catch the ones
that get through review.
-->

## What this changes, and why

<!--
The why matters more than the what; the diff already says what. If it fixes
something, what did the failure look like from outside?
-->

## How you know it works

<!--
Which command you ran and what it printed. "task ci is green" is fine.
-->

- [ ] `task ci` passes locally

## The two that catch what review doesn't

- [ ] **I watched my test fail first.** Not as ceremony: this repository has
      shipped tests that passed with the signal deleted, a coverage gate that
      counted operations it never ran, and a fix for a rendering bug that did not
      exist. If you have not seen your test go red for the reason you think it
      goes red, you do not yet know what it asserts.

- [ ] **Something calls what I added.** The most repeated defect here is code
      that is present, correct and wired to nothing: a validator nobody invoked,
      a tool implemented but never declared, a parameter agents were told about
      that no handler read. If you added a tool parameter, a config key or an
      exported function, say what reaches it.

## Anything you're unsure about

<!--
Genuinely useful: say so rather than hoping it goes unnoticed. A patch that
says "I could not work out how to test this part" gets help; one that hides it
gets found out later by somebody with less context.
-->
