// What an HTML artifact's locked frame is (issues #402 and #737). The page is loaded by its address under the
// frame's token (useFramePage), so that the server's answer carries its policy as a header: a srcdoc frame took the
// web client's own policy on top of the page's (Onyx's HtmlFilePreview.tsx loads its preview so, src and
// sandbox="allow-scripts"). The frame is sandboxed without allow-same-origin, so the page has an opaque origin: no
// cookies, no storage, no call with the owner's session.

// frameSandbox is what the frame may do: run its scripts and its forms, never be of this origin, open popups or
// move the top window (OpenMausBot's APP_FRAME_SANDBOX, src/components/app/app-frame-host.ts:6). The server's
// policy names the same flags.
export const frameSandbox = "allow-scripts allow-forms"
