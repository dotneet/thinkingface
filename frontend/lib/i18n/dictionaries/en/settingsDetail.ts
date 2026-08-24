// settingsDetail: copy for the expandable detail panels inside the settings
// screens — the stored value of an SSH key, a webhook delivery's response body.
// NOTE: the en dictionary is the source of truth for shape, so it must not be `as const`.
export const settingsDetail = {
  sshKeys: {
    // Deliberately constant in both states (the chevron carries open/closed):
    // a label that changed between "Show"/"Hide" would resize the button and
    // shove the Delete button next to it sideways — DESIGN.md §8.
    publicKeyToggle: "Public key",
    publicKeyToggleAria: "Public key for {title}",
    copyPublicKey: "Copy public key",
  },
  deliveries: {
    viewResponse: "View",
    viewResponseAria: "View the response for this delivery",
    responseTitle: "Delivery response",
    metaEvent: "Event",
    metaStatus: "Status",
    httpStatus: "HTTP {status}",
    bodyLabel: "Response body",
    copyResponse: "Copy response body",
    notAttemptedTitle: "Not delivered yet",
    notAttemptedBody:
      "This delivery is still queued, so there is no response to show yet. Redeliver it to send it now.",
    noResponseTitle: "No response received",
    noResponseBody:
      "The request never reached the endpoint, or it timed out before replying, so nothing was stored. That is also why the response column has no status code.",
    emptyBodyTitle: "Empty response body",
    emptyBodyBody: "The endpoint replied with HTTP {status} but sent no body at all.",
    truncationHint: "Only the first 4 KiB of the response is stored.",
  },
};
